package web

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/optimumsage/superkube/internal/audit"
)

// apiAuditPath returns the audit log file path.
func (s *Server) apiAuditPath(w http.ResponseWriter, _ *http.Request) {
	s.render.JSON(w, http.StatusOK, map[string]string{"path": audit.LogPath()})
}

// apiAuditList reads + filters the JSONL audit log and returns the matching
// rows. We always read the whole file then filter — typical audit logs are
// well under 10MB (the rotation size), so streaming filtering isn't worth
// the complexity here.
func (s *Server) apiAuditList(w http.ResponseWriter, r *http.Request) {
	filters := parseAuditFilters(r)
	rows, err := readAuditFiltered(filters)
	if err != nil && !os.IsNotExist(err) {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.render.JSON(w, http.StatusOK, map[string]any{"entries": rows})
}

// apiAuditStats summarizes filtered rows by verb / context / exit code.
func (s *Server) apiAuditStats(w http.ResponseWriter, r *http.Request) {
	filters := parseAuditFilters(r)
	rows, err := readAuditFiltered(filters)
	if err != nil && !os.IsNotExist(err) {
		s.render.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	byVerb := map[string]int{}
	byCtx := map[string]int{}
	failed := 0
	for _, e := range rows {
		byVerb[e.Verb]++
		if e.Context != "" {
			byCtx[e.Context]++
		}
		if e.ExitCode != 0 {
			failed++
		}
	}
	s.render.JSON(w, http.StatusOK, map[string]any{
		"total":      len(rows),
		"by_verb":    byVerb,
		"by_context": byCtx,
		"failed":     failed,
	})
}

// streamAudit tails the audit log file. Initial dump + tail-follow loop.
func (s *Server) streamAudit(w http.ResponseWriter, r *http.Request) {
	follow := r.URL.Query().Get("follow") == "1"
	filters := parseAuditFilters(r)

	sse, err := newSSE(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stop := sse.Heartbeat(15 * time.Second)
	defer stop()

	rows, err := readAuditFiltered(filters)
	if err != nil && !os.IsNotExist(err) {
		_ = sse.Send("error", map[string]string{"message": err.Error()})
		return
	}
	for _, e := range rows {
		_ = sse.Send("entry", e)
	}
	if !follow {
		_ = sse.Send("end", map[string]string{"reason": "snapshot"})
		return
	}

	// Tail: poll file growth.
	f, err := os.Open(audit.LogPath())
	if err != nil {
		_ = sse.Send("end", map[string]string{"reason": "no log file"})
		return
	}
	defer f.Close()
	pos, _ := f.Seek(0, io.SeekEnd)
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			info, err := os.Stat(audit.LogPath())
			if err != nil || info.Size() <= pos {
				continue
			}
			if _, err := f.Seek(pos, io.SeekStart); err != nil {
				return
			}
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for sc.Scan() {
				var e audit.Event
				if err := json.Unmarshal(sc.Bytes(), &e); err == nil && matchAudit(e, filters) {
					_ = sse.Send("entry", e)
				}
			}
			pos = info.Size()
		}
	}
}

// auditFilters captures the same query knobs `sk audit log` offers.
type auditFilters struct {
	Since      time.Time
	Verb       string
	Context    string
	FailedOnly bool
	Last       int
}

func parseAuditFilters(r *http.Request) auditFilters {
	q := r.URL.Query()
	f := auditFilters{
		Verb:       q.Get("verb"),
		Context:    q.Get("context"),
		FailedOnly: q.Get("failed") == "1",
	}
	if d := q.Get("since"); d != "" {
		dur, err := time.ParseDuration(d)
		if err == nil {
			f.Since = time.Now().Add(-dur)
		}
	}
	if n := q.Get("last"); n != "" {
		f.Last, _ = strconv.Atoi(n)
	}
	return f
}

func matchAudit(e audit.Event, f auditFilters) bool {
	if f.Verb != "" && !strings.EqualFold(e.Verb, f.Verb) {
		return false
	}
	if f.Context != "" && e.Context != f.Context {
		return false
	}
	if f.FailedOnly && e.ExitCode == 0 {
		return false
	}
	if !f.Since.IsZero() && e.Timestamp.Before(f.Since) {
		return false
	}
	return true
}

// readAuditFiltered reads the audit log and returns events matching filters,
// optionally truncated to filters.Last most-recent.
func readAuditFiltered(f auditFilters) ([]audit.Event, error) {
	file, err := os.Open(audit.LogPath())
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var out []audit.Event
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var e audit.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue
		}
		if matchAudit(e, f) {
			out = append(out, e)
		}
	}
	if f.Last > 0 && len(out) > f.Last {
		out = out[len(out)-f.Last:]
	}
	return out, sc.Err()
}
