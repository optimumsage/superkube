package cli

import (
	"path/filepath"
	"testing"
	"time"
)

// TestWebServiceStateRoundTrip ensures the sidecar JSON written at install
// can be read back unchanged at status time. We isolate XDG_STATE_HOME so
// the test doesn't trample a real install.
func TestWebServiceStateRoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	got, has, err := loadWebServiceState()
	if err != nil {
		t.Fatalf("load (empty) err: %v", err)
	}
	if has {
		t.Fatalf("expected no state in fresh tmpdir, got %+v", got)
	}

	want := webServiceState{
		Bind:        "127.0.0.1",
		Port:        7070,
		Token:       "deadbeef",
		Binary:      "/usr/local/bin/superkube",
		Platform:    "darwin",
		Label:       webServiceLabel,
		LogPath:     filepath.Join(t.TempDir(), "web.log"),
		ErrLogPath:  filepath.Join(t.TempDir(), "web.err"),
		InstalledAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := saveWebServiceState(want); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, has, err = loadWebServiceState()
	if err != nil || !has {
		t.Fatalf("load after save: err=%v has=%v", err, has)
	}
	// Compare the fields we care about. We don't compare InstalledAt
	// exactly because JSON serialization may round nanoseconds.
	if got.Bind != want.Bind || got.Port != want.Port || got.Token != want.Token ||
		got.Binary != want.Binary || got.Platform != want.Platform || got.Label != want.Label ||
		got.LogPath != want.LogPath || got.ErrLogPath != want.ErrLogPath {
		t.Errorf("roundtrip mismatch:\n got=%+v\nwant=%+v", got, want)
	}
	if !got.InstalledAt.Equal(want.InstalledAt) {
		t.Errorf("InstalledAt: got %v want %v", got.InstalledAt, want.InstalledAt)
	}

	if err := removeWebServiceState(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, has, _ := loadWebServiceState(); has {
		t.Errorf("state still present after remove")
	}
	// Removing again should be a no-op.
	if err := removeWebServiceState(); err != nil {
		t.Errorf("second remove should be idempotent, got %v", err)
	}
}
