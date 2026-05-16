package kubectl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// VersionInfo is the subset of `kubectl version --client -o json` that we
// actually need. We tolerate unknown fields.
type VersionInfo struct {
	Major        string `json:"major"`
	Minor        string `json:"minor"`
	GitVersion   string `json:"gitVersion"`
	GoVersion    string `json:"goVersion"`
	Platform     string `json:"platform"`
	BuildDate    string `json:"buildDate"`
	GitCommit    string `json:"gitCommit"`
	GitTreeState string `json:"gitTreeState"`
}

var (
	versionOnce sync.Once
	cachedVer   VersionInfo
	cachedErr   error
)

// Version probes the kubectl client version and caches the result for the
// lifetime of the process. Safe to call from multiple goroutines.
func (r *Runner) Version(ctx context.Context) (VersionInfo, error) {
	versionOnce.Do(func() {
		cachedVer, cachedErr = r.probeVersion(ctx)
	})
	return cachedVer, cachedErr
}

func (r *Runner) probeVersion(ctx context.Context) (VersionInfo, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, r.Path, "version", "--client", "-o", "json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return VersionInfo{}, fmt.Errorf("probe kubectl version: %w (stderr: %s)", err, stderr.String())
	}
	// `kubectl version --client -o json` wraps the data under "clientVersion"
	// in 1.27+; older builds put it at the top level.
	var wrapper struct {
		ClientVersion VersionInfo `json:"clientVersion"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &wrapper); err == nil && wrapper.ClientVersion.GitVersion != "" {
		return wrapper.ClientVersion, nil
	}
	var direct VersionInfo
	if err := json.Unmarshal(stdout.Bytes(), &direct); err != nil {
		return VersionInfo{}, fmt.Errorf("parse kubectl version output: %w", err)
	}
	return direct, nil
}

// SupportsServerDryRun reports whether the detected kubectl version supports
// `--dry-run=server` (added in 1.18). Falls back to true if version probe
// fails — better to attempt server dry-run and let kubectl error out cleanly
// than to silently downgrade to client dry-run.
func (r *Runner) SupportsServerDryRun(ctx context.Context) bool {
	v, err := r.Version(ctx)
	if err != nil {
		return true
	}
	major, minor := parseVersionNumber(v.GitVersion)
	if major < 1 {
		return true
	}
	if major == 1 && minor < 18 {
		return false
	}
	return true
}

// parseVersionNumber pulls major and minor out of strings like "v1.27.16".
func parseVersionNumber(s string) (major, minor int) {
	if len(s) == 0 {
		return 0, 0
	}
	if s[0] == 'v' {
		s = s[1:]
	}
	var maj, min int
	_, _ = fmt.Sscanf(s, "%d.%d", &maj, &min)
	return maj, min
}
