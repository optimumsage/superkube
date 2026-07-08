package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/optimumsage/superkube/internal/version"
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

// userAgent identifies superkube to GitHub. GitHub's API requires a User-Agent
// header and its abuse detection is friendlier to a descriptive one than to the
// generic Go default.
func userAgent() string {
	v := version.Version
	if v == "" {
		v = "dev"
	}
	return "superkube/" + v
}

// githubToken returns a personal-access / CI token from the environment, if
// present. Sending it lifts the rate limit from 60 to 5000 requests/hour. This
// is best-effort: unauthenticated upgrades keep working (until the 60/hr limit
// is hit). Note that `sudo` strips the environment, so under sudo the token
// must be passed explicitly (e.g. `sudo GITHUB_TOKEN=… sk upgrade`).
func githubToken() string {
	if t := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); t != "" {
		return t
	}
	return strings.TrimSpace(os.Getenv("GH_TOKEN"))
}

// applyGitHubHeaders sets the headers every GitHub request should carry. The
// api flag adds the REST-only Accept + API-version headers; release-asset
// downloads on github.com don't need them. Go strips the Authorization header
// on cross-host redirects, so it's safe to attach even for download URLs that
// redirect to a CDN.
func applyGitHubHeaders(req *http.Request, api bool) {
	req.Header.Set("User-Agent", userAgent())
	if api {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	if tok := githubToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

// rateLimitError returns a helpful error when resp is a GitHub rate-limit
// rejection (403/429 with X-RateLimit-Remaining: 0), or nil otherwise. The
// unauthenticated limit is 60 req/hr per IP — easy to hit behind shared NAT or
// on repeated runs — so we point the user at GITHUB_TOKEN and the reset time.
func rateLimitError(resp *http.Response) error {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return nil
	}
	if resp.Header.Get("X-RateLimit-Remaining") != "0" {
		return nil
	}
	const hint = "set GITHUB_TOKEN (or GH_TOKEN) to raise it to 5000/hr (under sudo: `sudo GITHUB_TOKEN=… sk upgrade`)"
	if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
		if secs, perr := strconv.ParseInt(reset, 10, 64); perr == nil {
			if d := time.Until(time.Unix(secs, 0)); d > 0 {
				return fmt.Errorf("github API rate limit exceeded (60 req/hr unauthenticated); resets in ~%s — %s", d.Round(time.Minute), hint)
			}
		}
	}
	return fmt.Errorf("github API rate limit exceeded (60 req/hr unauthenticated) — %s", hint)
}

// latestRelease asks the GitHub REST API for the most recent release tag.
// We match install.sh: GET /repos/<repo>/releases/latest, read tag_name.
func latestRelease(ctx context.Context, h httpDoer, repo string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	applyGitHubHeaders(req, true)
	resp, err := client(h).Do(req)
	if err != nil {
		return "", err
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		if rlErr := rateLimitError(resp); rlErr != nil {
			return "", rlErr
		}
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
