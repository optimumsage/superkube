package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// httpDoer is the minimal subset of *http.Client we consume. Defined as an
// interface so tests can stub network calls without spinning up httptest.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// defaultHTTPClient is intentionally modest: a single client with a 30s
// connect/header timeout. Downloads stream and may take longer; for those we
// use ctx instead of the client timeout.
var defaultHTTPClient = &http.Client{Timeout: 30 * time.Second}

func client(h httpDoer) httpDoer {
	if h != nil {
		return h
	}
	return defaultHTTPClient
}

// latestRelease asks the GitHub REST API for the most recent release tag.
// We match install.sh: GET /repos/<repo>/releases/latest, read tag_name.
func latestRelease(ctx context.Context, h httpDoer, repo string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client(h).Do(req)
	if err != nil {
		return "", err
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github API returned %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode release JSON: %w", err)
	}
	if body.TagName == "" {
		return "", errors.New("release JSON missing tag_name")
	}
	return body.TagName, nil
}

// assetName produces the tarball filename goreleaser emits. Keep this in sync
// with .goreleaser.yaml's name_template. The short version is the tag minus a
// leading `v`, matching how install.sh and goreleaser both compute it.
func assetName(tag, os, arch string) string {
	short := stripV(tag)
	return fmt.Sprintf("superkube_%s_%s_%s.tar.gz", short, os, arch)
}

func assetURL(repo, tag, asset string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, asset)
}

func checksumURL(repo, tag string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/checksums.txt", repo, tag)
}

// normalizeTag ensures the tag has the GitHub-style leading `v`. We accept
// either form on input (--version v0.2.1 or --version 0.2.1) and always emit
// the canonical `v` form so URLs are consistent.
func normalizeTag(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if !strings.HasPrefix(s, "v") {
		return "v" + s
	}
	return s
}

func stripV(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "v")
}
