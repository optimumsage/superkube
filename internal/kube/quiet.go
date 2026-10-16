package kube

import (
	"io"
	"os"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
)

// QuietClientLogs silences klog (and client-go's contextual logger) so
// reflector/watch failures don't print straight to stderr over a full-screen
// TUI or an in-place redrawn table. The returned func restores logging.
// Callers are expected to surface errors through their own UI instead.
func QuietClientLogs() (restore func()) {
	klog.LogToStderr(false)
	klog.SetOutput(io.Discard)
	klog.SetLogger(logr.Discard())
	return func() {
		klog.ClearLogger()
		klog.SetOutput(os.Stderr)
		klog.LogToStderr(true)
	}
}
