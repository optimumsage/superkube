package web

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/optimumsage/superkube/internal/portforward"
)

// apiPFList returns active port-forwards (matches `sk pf` default).
func (s *Server) apiPFList(w http.ResponseWriter, r *http.Request) {
	entries, err := portforward.Load()
	if err != nil {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{
			"id":         e.ID,
			"target":     e.Target,
			"ports":      e.Ports,
			"namespace":  e.Namespace,
			"context":    e.Context,
			"pid":        e.PID,
			"started_at": e.StartedAt,
			"age":        humanAgeSeconds(int64(time.Since(e.StartedAt).Seconds())),
		})
	}
	s.render.JSON(w, http.StatusOK, map[string]any{"entries": out})
}

// apiPFStart launches a new background forward.
func (s *Server) apiPFStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target    string   `json:"target"`
		Ports     []string `json:"ports"`
		Namespace string   `json:"namespace"`
		Address   string   `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Target == "" || len(body.Ports) == 0 {
		s.render.Error(w, http.StatusBadRequest, "target + ports required")
		return
	}
	sess := s.readSession(r)
	if body.Namespace == "" {
		body.Namespace = sess.Namespace
	}
	e, err := portforward.Start(portforward.StartOpts{
		Target:     body.Target,
		Ports:      body.Ports,
		Namespace:  body.Namespace,
		Context:    sess.Context,
		Kubeconfig: sess.Kubeconfig,
		Address:    body.Address,
	})
	if err != nil {
		s.render.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	s.recordWebAudit("pf-start", []string{body.Target}, 0, 0)
	s.render.JSON(w, http.StatusOK, map[string]any{
		"id": e.ID, "pid": e.PID, "log_path": e.LogPath,
	})
}

// apiPFStop terminates a forward by id (or "all").
func (s *Server) apiPFStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stopped, err := portforward.Stop(id)
	if err != nil {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordWebAudit("pf-stop", []string{id}, 0, 0)
	s.render.JSON(w, http.StatusOK, map[string]any{"stopped": len(stopped)})
}

// streamPFLogs tails the file portforward.Start wrote to. Uses simple polling
// like `sk pf logs -f` because portforward output isn't event-driven.
func (s *Server) streamPFLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	entry, ok, err := portforward.FindByID(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	sse, err := newSSE(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stop := sse.Heartbeat(15 * time.Second)
	defer stop()

	f, err := os.Open(entry.LogPath)
	if err != nil {
		_ = sse.Send("error", map[string]string{"message": err.Error()})
		return
	}
	defer f.Close()

	// Initial dump.
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		_ = sse.Send("line", map[string]string{"line": sc.Text()})
	}
	pos, _ := f.Seek(0, io.SeekCurrent)
	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			info, statErr := os.Stat(entry.LogPath)
			if statErr != nil || info.Size() <= pos {
				continue
			}
			if _, err := f.Seek(pos, io.SeekStart); err != nil {
				return
			}
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				_ = sse.Send("line", map[string]string{"line": sc.Text()})
			}
			pos = info.Size()
		}
	}
}

// humanAgeSeconds formats a duration in seconds as the same compact "Xs/Xm/Xh
// /Xd" form the CLI's pf list uses.
func humanAgeSeconds(secs int64) string {
	switch {
	case secs < 60:
		return itoa64(secs) + "s"
	case secs < 3600:
		return itoa64(secs/60) + "m"
	case secs < 86400:
		return itoa64(secs/3600) + "h"
	default:
		return itoa64(secs/86400) + "d"
	}
}
