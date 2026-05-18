package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/optimumsage/superkube/internal/config"
)

// Label / unit name for the `sk web` background service. Kept here (rather
// than in internal/service) because the package is otherwise web-agnostic.
const (
	webServiceLabel    = "com.optimumsage.superkube.web"
	webServiceUnitName = "superkube-web"
)

// webServiceState is the sidecar JSON written alongside install. It captures
// the install args so `sk web status` can reconstruct the URL without
// re-parsing the plist/unit. The label field switches between platforms.
type webServiceState struct {
	Bind        string    `json:"bind"`
	Port        int       `json:"port"`
	Token       string    `json:"token,omitempty"`
	Binary      string    `json:"binary"`
	Platform    string    `json:"platform"`
	Label       string    `json:"label"`
	LogPath     string    `json:"log_path"`
	ErrLogPath  string    `json:"err_log_path"`
	InstalledAt time.Time `json:"installed_at"`
}

func webServiceStatePath() string {
	return filepath.Join(config.StateDir(), "web-service.json")
}

func webServiceLogPaths() (string, string) {
	dir := config.StateDir()
	return filepath.Join(dir, "web.log"), filepath.Join(dir, "web.err")
}

// webServiceLabelForPlatform returns the platform-appropriate identifier
// (reverse-DNS for launchd, kebab basename for systemd).
func webServiceLabelForPlatform() string {
	if runtime.GOOS == "linux" {
		return webServiceUnitName
	}
	return webServiceLabel
}

func saveWebServiceState(st webServiceState) error {
	if err := os.MkdirAll(filepath.Dir(webServiceStatePath()), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(webServiceStatePath(), body, 0o600)
}

func loadWebServiceState() (webServiceState, bool, error) {
	var st webServiceState
	body, err := os.ReadFile(webServiceStatePath())
	if errors.Is(err, os.ErrNotExist) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	if err := json.Unmarshal(body, &st); err != nil {
		return st, false, err
	}
	return st, true, nil
}

func removeWebServiceState() error {
	if err := os.Remove(webServiceStatePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// webServiceURL composes the public URL for the service from its state. We
// always assume loopback display host when bind is loopback, matching what
// internal/web/server.go does at runtime.
func webServiceURL(st webServiceState) string {
	host := st.Bind
	switch host {
	case "127.0.0.1", "localhost", "::1", "[::1]", "":
		host = "127.0.0.1"
	}
	url := "http://" + host + ":" + strconv.Itoa(st.Port)
	if st.Token != "" {
		url += "/?token=" + st.Token
	}
	return url
}
