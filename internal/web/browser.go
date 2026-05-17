package web

import (
	"os/exec"
	"runtime"
)

// openBrowser asks the desktop environment to open url in the user's default
// browser. Best-effort: a missing opener (no DISPLAY, no Open command on PATH)
// is surfaced via the returned error so the caller can fall back to printing
// the URL.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	// Detach: we don't want to block on a browser process or capture its
	// stdio. Start (not Run) returns as soon as fork succeeds.
	return cmd.Start()
}
