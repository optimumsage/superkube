package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestWebSubcommandsRegistered guards against regressions where someone
// adds another subcommand wiring path that bypasses install/uninstall/status
// or drops one of them.
func TestWebSubcommandsRegistered(t *testing.T) {
	root := NewRootCmd()
	web := findChildCmd(root, "web")
	if web == nil {
		t.Fatal("web command missing from root")
	}
	want := []string{"install", "uninstall", "status"}
	for _, name := range want {
		sub := findChildCmd(web, name)
		if sub == nil {
			t.Errorf("web has no %q subcommand", name)
			continue
		}
		if sub.Short == "" {
			t.Errorf("web %s has empty Short", name)
		}
	}
}

func TestWebInstallFlags(t *testing.T) {
	root := NewRootCmd()
	web := findChildCmd(root, "web")
	if web == nil {
		t.Fatal("web command missing from root")
	}
	install := findChildCmd(web, "install")
	if install == nil {
		t.Fatal("web install command missing")
	}
	wantFlags := []string{"bind", "port", "token", "force", "binary"}
	for _, name := range wantFlags {
		if install.Flag(name) == nil {
			t.Errorf("web install missing --%s flag", name)
		}
	}
}

func TestWebServiceLabelForPlatform(t *testing.T) {
	got := webServiceLabelForPlatform()
	if got == "" {
		t.Fatal("label is empty")
	}
	if got != webServiceLabel && got != webServiceUnitName {
		t.Errorf("unexpected label %q", got)
	}
}

func TestWebServiceURL(t *testing.T) {
	st := webServiceState{Bind: "127.0.0.1", Port: 7070}
	if got := webServiceURL(st); got != "http://127.0.0.1:7070" {
		t.Errorf("loopback URL: got %q", got)
	}
	st.Token = "abc"
	if got := webServiceURL(st); !strings.Contains(got, "?token=abc") {
		t.Errorf("URL missing token: %q", got)
	}
}

func TestIsLoopbackBind(t *testing.T) {
	for _, bind := range []string{"127.0.0.1", "localhost", "::1", "[::1]", ""} {
		if !isLoopbackBind(bind) {
			t.Errorf("%q should be loopback", bind)
		}
	}
	for _, bind := range []string{"0.0.0.0", "192.168.1.1", "example.com"} {
		if isLoopbackBind(bind) {
			t.Errorf("%q should NOT be loopback", bind)
		}
	}
}

func findChildCmd(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}
