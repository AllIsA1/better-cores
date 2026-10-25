// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

// The cores of the panel's tunnel protocols as this plugin sees them: whether each program is there and running
// (the panel's own status routes) and what the project it comes from published last (its GitHub releases).

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// core is one tunnel core of the panel. Repo is the GitHub project it comes from ("" when it is not known).
type core struct {
	ID   string // the panel's name for it, as in /tunnel/<ID>/status
	Name string
	Repo string // owner/name
}

var cores = []core{
	{"openflux", "OpenFlux", "p1neappleXpress/OpenFlux"},
	{"csqtt", "CSQTT", "amurcanov/csqtt"},
	{"mieru", "mieru", "enfein/mieru"},
	{"trusttunnel", "TrustTunnel", "TrustTunnel/TrustTunnel"},
	{"naive", "NaiveProxy", "klzgrad/naiveproxy"},
	{"anytls", "AnyTLS", "anytls/anytls-go"},
	{"mtproxy", "MTProxy", "9seconds/mtg"},
	{"olcrtc", "olcRTC", "openlibrecommunity/olcrtc"},
	{"qwdtt", "qWDTT", ""},
	{"tproxy", "Telegram WEB proxy", ""},
}

// release is the last release a project published.
type release struct {
	Tag         string    `json:"tag"`
	PublishedAt time.Time `json:"publishedAt"`
	URL         string    `json:"url"`
}

// coreStatus is what the page shows for one core.
type coreStatus struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Repo    string   `json:"repo,omitempty"`
	Present bool     `json:"present"`
	Running bool     `json:"running"`
	Path    string   `json:"path,omitempty"`
	Latest  *release `json:"latest,omitempty"`
	Error   string   `json:"error,omitempty"` // why the panel's status could not be read
}

type panelAPI interface {
	Do(ctx context.Context, method, path string, in, out any) error
}

// panelCoreStatus is the part of the panel's answer to /tunnel/<core>/status the plugin reads.
type panelCoreStatus struct {
	BinaryExists bool   `json:"binaryExists"`
	BinaryPath   string `json:"binaryPath"`
	Probe        struct {
		Running bool `json:"running"`
	} `json:"probe"`
}

// upstream finds the last release of a project; the plugin asks GitHub, tests ask a fake.
type upstream interface {
	latest(ctx context.Context, repo string) (release, error)
}

// reporter puts the panel's view and the projects' releases together.
type reporter struct {
	api   panelAPI
	up    upstream
	now   func() time.Time
	ttl   time.Duration // how long a release lookup is fresh
	retry time.Duration // how long to wait after a failed lookup

	mu       sync.Mutex
	releases map[string]*cached
}

type cached struct {
	rel      release
	ok       bool
	at       time.Time
	failedAt time.Time
	running  bool
}

func newReporter(api panelAPI, up upstream) *reporter {
	return &reporter{api: api, up: up, now: time.Now, ttl: time.Hour, retry: 10 * time.Minute, releases: map[string]*cached{}}
}

// report reads every core's status from the panel at once. The releases come from memory: a lookup that is missing
// or old is started in the background, so the page never waits for GitHub.
func (r *reporter) report(ctx context.Context) []coreStatus {
	out := make([]coreStatus, len(cores))
	var wg sync.WaitGroup
	for i, c := range cores {
		out[i] = coreStatus{ID: c.ID, Name: c.Name, Repo: c.Repo}
		if rel, ok := r.cachedRelease(c.Repo); ok {
			out[i].Latest = &rel
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			var st panelCoreStatus
			if err := r.api.Do(ctx, http.MethodGet, fmt.Sprintf("/tunnel/%s/status", c.ID), nil, &st); err != nil {
				out[i].Error = err.Error()
				return
			}
			out[i].Present, out[i].Running, out[i].Path = st.BinaryExists, st.Probe.Running, st.BinaryPath
		}()
	}
	wg.Wait()
	return out
}

// cachedRelease returns what is known of a project's last release and starts a lookup when it is missing or old.
func (r *reporter) cachedRelease(repo string) (release, bool) {
	if repo == "" {
		return release{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.releases[repo]
	if c == nil {
		c = &cached{}
		r.releases[repo] = c
	}
	now := r.now()
	fresh := c.ok && now.Sub(c.at) < r.ttl
	recentlyFailed := !c.failedAt.IsZero() && now.Sub(c.failedAt) < r.retry
	if !fresh && !recentlyFailed && !c.running {
		c.running = true
		go r.lookup(repo, c)
	}
	return c.rel, c.ok
}

func (r *reporter) lookup(repo string, c *cached) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rel, err := r.up.latest(ctx, repo)
	r.mu.Lock()
	defer r.mu.Unlock()
	c.running = false
	if err != nil {
		c.failedAt = r.now()
		return
	}
	c.rel, c.ok, c.at, c.failedAt = rel, true, r.now(), time.Time{}
}

// github asks the public releases API; the plugin needs the network permission for this and nothing else.
type github struct{ client *http.Client }

func newGithub() *github { return &github{client: &http.Client{Timeout: 12 * time.Second}} }

func (g *github) latest(ctx context.Context, repo string) (release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "better-cores")
	resp, err := g.client.Do(req)
	if err != nil {
		return release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return release{}, fmt.Errorf("GitHub answered HTTP %d for %s", resp.StatusCode, repo)
	}
	var body struct {
		Tag         string    `json:"tag_name"`
		PublishedAt time.Time `json:"published_at"`
		URL         string    `json:"html_url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 4<<20)).Decode(&body); err != nil {
		return release{}, err
	}
	if body.Tag == "" {
		return release{}, fmt.Errorf("%s has no release", repo)
	}
	return release{Tag: body.Tag, PublishedAt: body.PublishedAt, URL: body.URL}, nil
}
