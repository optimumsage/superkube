package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/service"
	"github.com/optimumsage/superkube/internal/ui"
)

// newWebInstallCmd registers `sk web install`, which writes a launchd
// (macOS) / systemd --user (Linux) unit and starts it. The service runs
// `superkube web` in the background with --no-open so it survives shell
// exit and reboots into the user session on next login.
func newWebInstallCmd() *cobra.Command {
	var (
		bind    string
		port    int
		token   string
		force   bool
		binPath string
	)
	c := &cobra.Command{
		Use:   "install",
		Short: "Install the web UI as a background service",
		Long: `Register sk web as a user-level OS service so it runs in the background
and auto-starts on login.

Backend: launchd LaunchAgent on macOS (~/Library/LaunchAgents) or systemd
--user unit on Linux (~/.config/systemd/user). No sudo required. The
service is started immediately; visit the printed URL in your browser.

To survive a full logout on Linux, run once:  loginctl enable-linger $USER`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, err := service.NewManager()
			if err != nil {
				return err
			}
			if bind == "" {
				bind = "127.0.0.1"
			}
			// Auto-pick a free port now (rather than letting the
			// server pick at start time) so the URL is known at
			// install time and saved to the sidecar JSON.
			if port == 0 {
				p, err := pickFreePort(bind)
				if err != nil {
					return fmt.Errorf("pick free port: %w", err)
				}
				port = p
			}
			if token == "" && !isLoopbackBind(bind) {
				token = randomHexToken(32)
			}
			if binPath == "" {
				binPath, err = resolveBinary()
				if err != nil {
					return err
				}
			}

			logPath, errLogPath := webServiceLogPaths()
			args := []string{
				"web",
				"--bind", bind,
				"--port", strconv.Itoa(port),
				"--no-open",
			}
			if token != "" {
				args = append(args, "--token", token)
			}
			spec := service.Spec{
				Label:      webServiceLabelForPlatform(),
				BinaryPath: binPath,
				Args:       args,
				Env:        passthroughEnv(),
				LogPath:    logPath,
				ErrLogPath: errLogPath,
				WorkingDir: "/",
			}

			if err := mgr.Install(spec, force); err != nil {
				if errors.Is(err, service.ErrAlreadyInstalled) {
					return fmt.Errorf("a web service is already installed; pass --force to replace it, or run `sk web uninstall` first")
				}
				return err
			}

			st := webServiceState{
				Bind:        bind,
				Port:        port,
				Token:       token,
				Binary:      binPath,
				Platform:    runtime.GOOS,
				Label:       spec.Label,
				LogPath:     logPath,
				ErrLogPath:  errLogPath,
				InstalledAt: time.Now().UTC(),
			}
			if err := saveWebServiceState(st); err != nil {
				// Non-fatal: install succeeded, sidecar is a
				// convenience for status. Warn but don't fail.
				fmt.Fprintln(os.Stderr, ui.Render(ui.Warning, "warning: failed to save web service state: "+err.Error()))
			}

			out := cmd.OutOrStdout()
			fmt.Fprintln(out, ui.Render(ui.Success, "installed web service: "+spec.Label))
			fmt.Fprintln(out, ui.Render(ui.Subtle, "unit:   "+mgr.UnitPath(spec.Label)))
			fmt.Fprintln(out, ui.Render(ui.Subtle, "stdout: "+logPath))
			fmt.Fprintln(out, ui.Render(ui.Subtle, "stderr: "+errLogPath))
			fmt.Fprintln(out, ui.Render(ui.Success, "url:    "+webServiceURL(st)))
			if runtime.GOOS == "linux" {
				fmt.Fprintln(out, ui.Render(ui.Subtle, "hint:   run `loginctl enable-linger $USER` so the service survives logout"))
			}
			fmt.Fprintln(out, ui.Render(ui.Subtle, "check:  sk web status"))
			return nil
		},
	}
	c.Flags().StringVar(&bind, "bind", "127.0.0.1", "bind address; use 0.0.0.0 for non-loopback only on trusted networks")
	c.Flags().IntVar(&port, "port", 0, "TCP port (0 picks a free port at install time)")
	c.Flags().StringVar(&token, "token", "", "auth token; auto-generated when --bind is non-loopback")
	c.Flags().BoolVar(&force, "force", false, "replace an existing install instead of failing")
	c.Flags().StringVar(&binPath, "binary", "", "absolute path to the superkube binary the service should run (default: this binary)")
	return c
}

// pickFreePort reserves and immediately releases a TCP port on bind. There
// is a small race between release and the service binding it, but the port
// is in TIME_WAIT/REUSE territory for our local case and launchd/systemd
// will retry on failure (KeepAlive / Restart=on-failure).
func pickFreePort(bind string) (int, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, "0"))
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// resolveBinary returns the absolute path of the currently running
// superkube binary. We refuse to install a service that points at a
// non-absolute or non-executable path because launchd/systemd will silently
// fail to start it.
func resolveBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve own binary: %w", err)
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		return "", fmt.Errorf("absolute path: %w", err)
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		// EvalSymlinks fails if the binary was unlinked since launch;
		// fall through to the abs path and let the OS error speak.
		abs, _ = filepath.Abs(exe)
	}
	if fi, err := os.Stat(abs); err != nil || fi.IsDir() {
		return "", fmt.Errorf("binary %q is not a file", abs)
	}
	return abs, nil
}

func randomHexToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Extremely unlikely; fall back to time-based pseudo-random
		// so we never ship an empty token.
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func isLoopbackBind(bind string) bool {
	switch bind {
	case "127.0.0.1", "localhost", "::1", "[::1]", "":
		return true
	}
	return false
}

// passthroughEnv selects environment variables to copy into the service.
// We intentionally pass only the things `superkube web` actually needs
// (KUBECONFIG, HOME, PATH); everything else stays out of the unit file.
func passthroughEnv() map[string]string {
	keys := []string{"KUBECONFIG", "HOME", "PATH", "USER", "LOGNAME"}
	env := make(map[string]string, len(keys))
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	return env
}
