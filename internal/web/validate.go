package web

import (
	"net"
	"net/http"
	"regexp"
	"strings"
)

// Every string a request can put into a kubectl/helm argv must be validated
// before it gets there. badToken (handlers_destructive.go) rejects empty and
// flag-shaped values; the helpers below add the stricter shapes needed where
// kubectl parses a positional (port-forward target/ports/address).

var (
	targetRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.\-]*/[A-Za-z0-9][A-Za-z0-9._\-]*$`)
	portRe   = regexp.MustCompile(`^(\d{1,5}(:\d{1,5})?|:\d{1,5})$`)
)

// optBad is badToken for optional values: empty is fine, flag-shaped is not.
func optBad(vals ...string) bool {
	for _, v := range vals {
		if v != "" && badToken(v) {
			return true
		}
	}
	return false
}

// badInput writes a 400 and returns true when any value is empty/flag-shaped.
func (s *Server) badInput(w http.ResponseWriter, vals ...string) bool {
	if badToken(vals...) {
		s.render.Error(w, http.StatusBadRequest, "invalid or flag-shaped argument")
		return true
	}
	return false
}

// badOptInput is badInput for optional values (empty allowed).
func (s *Server) badOptInput(w http.ResponseWriter, vals ...string) bool {
	if optBad(vals...) {
		s.render.Error(w, http.StatusBadRequest, "invalid or flag-shaped argument")
		return true
	}
	return false
}

func validPortForwardTarget(t string) bool { return targetRe.MatchString(t) }

func validPortSpec(p string) bool { return portRe.MatchString(p) }

// validPFAddress accepts a comma-separated list of IPs and "localhost".
func validPFAddress(a string) bool {
	if a == "" {
		return true
	}
	for _, part := range strings.Split(a, ",") {
		if part != "localhost" && net.ParseIP(part) == nil {
			return false
		}
	}
	return true
}

// badRevision rejects a ?revision= that is not a plain positive integer.
func (s *Server) badRevision(w http.ResponseWriter, r *http.Request) bool {
	rev := r.URL.Query().Get("revision")
	if rev == "" {
		return false
	}
	if strings.Trim(rev, "0") == "" {
		s.render.Error(w, http.StatusBadRequest, "invalid revision")
		return true
	}
	for _, c := range rev {
		if c < '0' || c > '9' {
			s.render.Error(w, http.StatusBadRequest, "invalid revision")
			return true
		}
	}
	return false
}
