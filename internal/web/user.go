package web

import "os"

// webUser returns the audit-log "user" field for actions taken through the
// web UI. We tag with (web) so it's easy to distinguish from CLI invocations
// in the shared audit log.
func webUser() string {
	u := os.Getenv("USER")
	if u == "" {
		u = os.Getenv("LOGNAME")
	}
	if u == "" {
		u = "anonymous"
	}
	return u + " (web)"
}
