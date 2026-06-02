// Package updater checks GitHub Releases for a newer aimonitor build and
// describes how to install it. It never installs anything itself — the CLI
// (`aimonitor update install`) owns the install side effect — so this
// package is pure, network-only, and safe to call from anywhere.
//
// Why list releases instead of /releases/latest: aimonitor ships
// pre-releases (the v1.0.0-beta.N line), and GitHub's "latest" endpoint
// excludes pre-releases. We fetch the recent releases and pick the newest
// published (non-draft) tag, including pre-releases, then compare it to the
// running version.
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Repo is the GitHub owner/repo aimonitor releases live under.
const Repo = "japananh/aimonitor"

// HTMLURL is the human-facing releases page, surfaced in the "view on
// GitHub" affordance and as a fallback when an install can't proceed.
const HTMLURL = "https://github.com/" + Repo + "/releases"

const releasesAPI = "https://api.github.com/repos/" + Repo + "/releases?per_page=10"

// checkTimeout bounds one CheckLatest round-trip. The check is best-effort
// background work; we never want it to hang a UI action or a daemon tick.
const checkTimeout = 10 * time.Second

// Info is the result of a check: whether a newer release exists and the
// metadata needed to render a prompt.
type Info struct {
	// Available is true only when Latest is a strictly newer version than
	// Current. Equal or older (a dev build ahead of the last release) is
	// not an update.
	Available bool   `json:"available"`
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	// URL is the release's GitHub page (notes + assets).
	URL string `json:"url"`
	// Notes is the release body (markdown), trimmed by the caller for
	// display. May be empty.
	Notes string `json:"notes,omitempty"`
}

type ghRelease struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Body       string `json:"body"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// Checker queries GitHub. The zero value is usable; HTTP defaults to a
// client with checkTimeout. APIURL is overridable for tests.
type Checker struct {
	HTTP   *http.Client
	APIURL string
}

// CheckLatest reports whether a release newer than current exists. current
// is the running version string (e.g. version.Version, "v1.0.0-beta.10" or
// "1.0.0-beta.10"; a leading "v" is optional). A "dev" or unparseable
// current is treated as "older than any release" so developers still see
// that a release exists, but never as an error.
func (c *Checker) CheckLatest(ctx context.Context, current string) (Info, error) {
	api := c.APIURL
	if api == "" {
		api = releasesAPI
	}
	httpc := c.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: checkTimeout}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return Info{}, fmt.Errorf("update check: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "aimonitor-updater")

	resp, err := httpc.Do(req)
	if err != nil {
		return Info{}, fmt.Errorf("update check: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Info{}, fmt.Errorf("update check: GitHub HTTP %d", resp.StatusCode)
	}

	var releases []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return Info{}, fmt.Errorf("update check: decode: %w", err)
	}

	latest, ok := newestPublished(releases)
	if !ok {
		// No published releases at all — not an error, just nothing to do.
		return Info{Available: false, Current: current}, nil
	}

	info := Info{
		Current: current,
		Latest:  latest.TagName,
		URL:     latest.HTMLURL,
		Notes:   latest.Body,
	}
	info.Available = compareSemver(current, latest.TagName) < 0
	return info, nil
}

// newestPublished returns the highest-versioned non-draft release. GitHub
// returns releases newest-first, but we compare explicitly rather than
// trusting order, so an out-of-order publish can't pick a wrong tag.
func newestPublished(releases []ghRelease) (ghRelease, bool) {
	var best ghRelease
	found := false
	for _, r := range releases {
		if r.Draft || r.TagName == "" {
			continue
		}
		if !found || compareSemver(best.TagName, r.TagName) < 0 {
			best = r
			found = true
		}
	}
	return best, found
}
