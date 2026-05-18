// Package version exposes build-time identifying information for the binary.
// Values are injected via -ldflags at build time (see Makefile).
package version

var (
	Version = "1.0.2"
	Commit  = "none"
	Date    = "unknown"
)

func String() string {
	return Version + " (" + Commit + ", " + Date + ")"
}
