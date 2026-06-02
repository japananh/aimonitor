package updater

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.0.0-beta.9", "v1.0.0-beta.10", -1}, // numeric pre-release, not lexical
		{"v1.0.0-beta.10", "v1.0.0-beta.9", 1},
		{"v1.0.0-beta.10", "v1.0.0-beta.10", 0},
		{"1.0.0-beta.10", "v1.0.0-beta.10", 0}, // leading v optional
		{"v1.0.0-beta.10", "v1.0.0", -1},       // pre-release < release
		{"v1.0.0", "v1.0.0-beta.10", 1},
		{"v1.2.0", "v1.10.0", -1}, // numeric core, not lexical
		{"v2.0.0", "v1.9.9", 1},
		{"dev", "v1.0.0-beta.10", -1}, // unparseable sorts lowest
		{"dev", "dev", 0},
	}
	for _, c := range cases {
		if got := compareSemver(c.a, c.b); got != c.want {
			t.Errorf("compareSemver(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCheckLatest_PicksNewestIncludingPrerelease(t *testing.T) {
	// GitHub-shaped payload: a draft (ignored) and two published pre-releases.
	body := `[
		{"tag_name":"v1.0.0-beta.99","draft":true,"prerelease":true,"html_url":"x"},
		{"tag_name":"v1.0.0-beta.11","draft":false,"prerelease":true,"html_url":"https://gh/rel/11","body":"notes"},
		{"tag_name":"v1.0.0-beta.10","draft":false,"prerelease":true,"html_url":"https://gh/rel/10"}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := &Checker{APIURL: srv.URL, HTTP: srv.Client()}

	// Running an older version → update available, newest non-draft chosen.
	got, err := c.CheckLatest(context.Background(), "v1.0.0-beta.10")
	if err != nil {
		t.Fatalf("CheckLatest: %v", err)
	}
	if !got.Available || got.Latest != "v1.0.0-beta.11" {
		t.Fatalf("got %+v, want available v1.0.0-beta.11", got)
	}
	if got.URL != "https://gh/rel/11" || got.Notes != "notes" {
		t.Errorf("metadata = %+v", got)
	}

	// Running the newest already → not available.
	got, err = c.CheckLatest(context.Background(), "v1.0.0-beta.11")
	if err != nil {
		t.Fatalf("CheckLatest: %v", err)
	}
	if got.Available {
		t.Errorf("should not offer an update when already on newest: %+v", got)
	}
}

func TestCheckLatest_NoReleases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := &Checker{APIURL: srv.URL, HTTP: srv.Client()}
	got, err := c.CheckLatest(context.Background(), "v1.0.0-beta.10")
	if err != nil {
		t.Fatalf("CheckLatest: %v", err)
	}
	if got.Available {
		t.Errorf("no releases should mean no update: %+v", got)
	}
}
