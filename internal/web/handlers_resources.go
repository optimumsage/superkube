package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/kubectl"
)

// apiResourceList renders a Table-shaped LIST snapshot for any kubectl-known
// resource kind. The client uses this for the initial fill; live updates come
// from the SSE watch endpoint.
func (s *Server) apiResourceList(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	sess := s.readSession(r)
	allNS := r.URL.Query().Get("all") == "1" || r.URL.Query().Get("allNamespaces") == "true"
	selector := r.URL.Query().Get("selector")

	frame, err := s.fetchTableOnce(r.Context(), sess, kind, allNS, selector)
	if err != nil {
		s.render.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	s.render.JSON(w, http.StatusOK, map[string]any{
		"kind":    kind,
		"headers": frame.Headers,
		"rows":    frame.Rows,
	})
}

// fetchTableOnce runs WatchTable for exactly one render then cancels. That
// reuses the same Table-shaped LIST + GVR-resolution kube.WatchTable already
// has; duplicating it inline would drift over time.
func (s *Server) fetchTableOnce(parent context.Context, sess session, kind string, allNS bool, selector string) (kube.TableFrame, error) {
	loader := s.loader(sess)
	ns := sess.Namespace
	if allNS {
		ns = ""
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	captured := make(chan kube.TableFrame, 1)
	errs := make(chan error, 1)
	go func() {
		err := loader.WatchTable(ctx, kube.WatchTableOpts{
			Resource:      kind,
			Namespace:     ns,
			AllNamespaces: allNS,
			Selector:      selector,
		}, func(f kube.TableFrame) {
			select {
			case captured <- f:
			default:
			}
			cancel()
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			errs <- err
		} else {
			errs <- nil
		}
	}()
	select {
	case f := <-captured:
		return f, nil
	case err := <-errs:
		if err != nil {
			return kube.TableFrame{}, err
		}
		return kube.TableFrame{}, fmt.Errorf("no rows returned for %q", kind)
	case <-time.After(8 * time.Second):
		return kube.TableFrame{}, fmt.Errorf("resource list timed out for %q", kind)
	}
}

// streamWatch is the SSE endpoint powering live table views. Each watch event
// becomes one "replace" SSE event with the full frame; clients render only
// the latest frame.
func (s *Server) streamWatch(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	sess := s.readSession(r)
	allNS := r.URL.Query().Get("all") == "1"
	selector := r.URL.Query().Get("selector")

	sse, err := newSSE(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stop := sse.Heartbeat(15 * time.Second)
	defer stop()

	loader := s.loader(sess)
	ns := sess.Namespace
	if allNS {
		ns = ""
	}

	err = loader.WatchTable(r.Context(), kube.WatchTableOpts{
		Resource:      kind,
		Namespace:     ns,
		AllNamespaces: allNS,
		Selector:      selector,
	}, func(f kube.TableFrame) {
		_ = sse.Send("replace", map[string]any{
			"headers": f.Headers,
			"rows":    f.Rows,
		})
	})
	if err != nil && r.Context().Err() == nil {
		_ = sse.Send("error", map[string]string{"message": err.Error()})
	}
	_ = sse.Send("end", map[string]string{"reason": "context-cancelled"})
}

// apiResourceDescribe shells out to `kubectl describe <kind> <name>` and
// returns plain text.
func (s *Server) apiResourceDescribe(w http.ResponseWriter, r *http.Request) {
	s.captureKubectlText(w, r, []string{"describe", r.PathValue("kind"), r.PathValue("name"), "-n", r.PathValue("ns")})
}

// apiResourceYAML returns the resource's full YAML manifest as it lives in
// the cluster.
func (s *Server) apiResourceYAML(w http.ResponseWriter, r *http.Request) {
	s.captureKubectlText(w, r, []string{"get", r.PathValue("kind"), r.PathValue("name"), "-n", r.PathValue("ns"), "-o", "yaml"})
}

// apiResourceEvents lists events for the resource using kubectl events --for.
func (s *Server) apiResourceEvents(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	name := r.PathValue("name")
	ns := r.PathValue("ns")
	s.captureKubectlText(w, r, []string{"events", "--for", kind + "/" + name, "-n", ns})
}

// captureKubectlText runs kubectl with the given args, captures stdout +
// stderr together, and returns text/plain. Context overrides from the session
// are prepended first.
func (s *Server) captureKubectlText(w http.ResponseWriter, r *http.Request, args []string) {
	sess := s.readSession(r)
	// Strip session ns when caller already supplied -n explicitly.
	full := args
	if hasFlagN(args) {
		full = sess.prependGlobalFlagsNoNS(args)
	} else {
		full = sess.prependGlobalFlags(args)
	}
	var out bytes.Buffer
	err := s.deps.Runner.Run(r.Context(), full, kubectl.RunOpts{Stdout: &out, Stderr: &out})
	if err != nil && !isExit1(err) {
		if strings.TrimSpace(out.String()) == "" {
			s.render.Error(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(out.Bytes())
}

// hasFlagN reports whether args already contains -n / --namespace.
func hasFlagN(args []string) bool {
	for _, a := range args {
		if a == "-n" || a == "--namespace" || strings.HasPrefix(a, "--namespace=") {
			return true
		}
	}
	return false
}

// isExit1 reports whether err is a kubectl exit-code-1 error. We treat it as
// "ran but produced no rows / not-found", whose output is still useful.
func isExit1(err error) bool {
	var ee *kubectl.ExitCodeError
	if errors.As(err, &ee) {
		return ee.Code == 1
	}
	return false
}
