// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakePanel struct {
	mu     sync.Mutex
	status map[string]panelCoreStatus
	fail   map[string]error
	calls  []string
}

func (p *fakePanel) Do(_ context.Context, method, path string, _ any, out any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, method+" "+path)
	if method != http.MethodGet || !strings.HasPrefix(path, "/tunnel/") || !strings.HasSuffix(path, "/status") {
		return errors.New("the plugin asked for " + method + " " + path + ", which it has no business with")
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, "/tunnel/"), "/status")
	if err := p.fail[id]; err != nil {
		return err
	}
	*(out.(*panelCoreStatus)) = p.status[id]
	return nil
}

type fakeUpstream struct {
	mu    sync.Mutex
	rel   map[string]release
	err   error
	calls map[string]int
	slow  time.Duration
}

func (u *fakeUpstream) latest(_ context.Context, repo string) (release, error) {
	if u.slow > 0 {
		time.Sleep(u.slow)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.calls == nil {
		u.calls = map[string]int{}
	}
	u.calls[repo]++
	if u.err != nil {
		return release{}, u.err
	}
	return u.rel[repo], nil
}

func (u *fakeUpstream) count(repo string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls[repo]
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func byID(list []coreStatus, id string) coreStatus {
	for _, c := range list {
		if c.ID == id {
			return c
		}
	}
	return coreStatus{}
}

func rig() (*reporter, *fakePanel, *fakeUpstream) {
	p := &fakePanel{status: map[string]panelCoreStatus{}, fail: map[string]error{}}
	var mieru panelCoreStatus
	mieru.BinaryExists, mieru.BinaryPath = true, "/usr/local/x-ui/bin/mieru-linux-amd64"
	mieru.Probe.Running = true
	p.status["mieru"] = mieru
	p.status["csqtt"] = panelCoreStatus{BinaryExists: true}
	u := &fakeUpstream{rel: map[string]release{"enfein/mieru": {Tag: "v3.39.0", URL: "https://github.com/enfein/mieru/releases/tag/v3.39.0"}}}
	return newReporter(p, u), p, u
}

func TestEveryCoreIsReportedFromThePanelsOwnStatus(t *testing.T) {
	r, _, _ := rig()
	list := r.report(context.Background())
	if len(list) != len(cores) {
		t.Fatalf("%d cores reported, want %d", len(list), len(cores))
	}
	if m := byID(list, "mieru"); !m.Present || !m.Running || m.Path == "" || m.Name != "mieru" || m.Repo != "enfein/mieru" {
		t.Errorf("mieru = %+v", m)
	}
	if c := byID(list, "csqtt"); !c.Present || c.Running {
		t.Errorf("csqtt = %+v: there, but not running", c)
	}
	if n := byID(list, "naive"); n.Present || n.Running || n.Error != "" {
		t.Errorf("naive = %+v: a core the panel reports as missing", n)
	}
}

func TestOnlyStatusRoutesAreAskedAndOneBrokenCoreSpoilsNoOther(t *testing.T) {
	r, p, _ := rig()
	p.fail["anytls"] = errors.New("panel answered HTTP 500")
	list := r.report(context.Background())
	if a := byID(list, "anytls"); a.Error == "" || a.Present {
		t.Errorf("anytls = %+v, want its error", a)
	}
	if !byID(list, "mieru").Present {
		t.Error("a failing core hid another one")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.calls {
		if !strings.HasPrefix(c, "GET /tunnel/") || !strings.HasSuffix(c, "/status") {
			t.Errorf("the plugin called %s", c)
		}
	}
	if len(p.calls) != len(cores) {
		t.Errorf("%d calls for %d cores", len(p.calls), len(cores))
	}
}

func TestReleasesComeInTheBackgroundAndThePageNeverWaits(t *testing.T) {
	r, _, u := rig()
	u.slow = 300 * time.Millisecond
	start := time.Now()
	first := r.report(context.Background())
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Errorf("the first report waited %v for GitHub", d)
	}
	if byID(first, "mieru").Latest != nil {
		t.Error("a release that was not looked up yet was shown")
	}
	waitFor(t, func() bool { return byID(r.report(context.Background()), "mieru").Latest != nil })
	if got := byID(r.report(context.Background()), "mieru").Latest; got.Tag != "v3.39.0" {
		t.Errorf("latest = %+v", got)
	}
	if byID(r.report(context.Background()), "qwdtt").Latest != nil {
		t.Error("a core with no known project got a release")
	}
}

func TestAProjectIsAskedOncePerHourAndAFailureIsNotHammered(t *testing.T) {
	r, _, u := rig()
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	r.report(context.Background())
	waitFor(t, func() bool { return u.count("enfein/mieru") == 1 })
	for i := 0; i < 5; i++ {
		r.report(context.Background())
	}
	if n := u.count("enfein/mieru"); n != 1 {
		t.Errorf("asked %d times within the hour", n)
	}
	now = now.Add(r.ttl + time.Minute)
	r.report(context.Background())
	waitFor(t, func() bool { return u.count("enfein/mieru") == 2 })

	u.mu.Lock()
	u.err = errors.New("rate limited")
	u.mu.Unlock()
	now = now.Add(r.ttl + time.Minute)
	for i := 0; i < 10; i++ {
		r.report(context.Background())
	}
	waitFor(t, func() bool { return u.count("enfein/mieru") == 3 })
	time.Sleep(50 * time.Millisecond)
	if n := u.count("enfein/mieru"); n != 3 {
		t.Errorf("a failing lookup was repeated at once (%d)", n)
	}
	if byID(r.report(context.Background()), "mieru").Latest == nil {
		t.Error("the last good release was dropped because a lookup failed")
	}
	now = now.Add(r.retry + time.Second)
	u.mu.Lock()
	u.err = nil
	u.mu.Unlock()
	r.report(context.Background())
	waitFor(t, func() bool { return u.count("enfein/mieru") == 4 })
}

func TestRequestsThatArriveTogetherStartOneLookup(t *testing.T) {
	r, _, u := rig()
	u.slow = 150 * time.Millisecond
	for i := 0; i < 8; i++ {
		r.report(context.Background())
	}
	waitFor(t, func() bool { return byID(r.report(context.Background()), "mieru").Latest != nil })
	if n := u.count("enfein/mieru"); n != 1 {
		t.Errorf("%d lookups of one project", n)
	}
}

func TestGithubReadsTheLatestRelease(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/repos/some/untagged/releases/latest" {
			_, _ = w.Write([]byte(`{"name":"no tag"}`))
			return
		}
		if req.URL.Path != "/repos/enfein/mieru/releases/latest" {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v3.39.0","published_at":"2026-10-01T10:00:00Z","html_url":"https://github.com/enfein/mieru/releases/tag/v3.39.0"}`))
	}))
	defer srv.Close()
	g := &github{client: srv.Client()}
	g.client.Transport = rewrite{srv.Client().Transport, strings.TrimPrefix(srv.URL, "https://")}
	rel, err := g.latest(context.Background(), "enfein/mieru")
	if err != nil || rel.Tag != "v3.39.0" || rel.PublishedAt.Year() != 2026 || !strings.HasPrefix(rel.URL, "https://github.com/") {
		t.Fatalf("release = %+v, %v", rel, err)
	}
	if _, err := g.latest(context.Background(), "some/untagged"); err == nil {
		t.Error("an answer without a tag was taken for a release")
	}
	if _, err := g.latest(context.Background(), "nobody/nothing"); err == nil {
		t.Error("a missing project was not an error")
	}
}

// rewrite sends every request to the test server, whatever host it names.
type rewrite struct {
	inner http.RoundTripper
	host  string
}

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Host = r.host
	return r.inner.RoundTrip(req)
}

func TestTheTableNamesEachCoreOnceAndOnlyKnownRepositories(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range cores {
		if seen[c.ID] {
			t.Errorf("%s listed twice", c.ID)
		}
		seen[c.ID] = true
		if c.Repo != "" && strings.Count(c.Repo, "/") != 1 {
			t.Errorf("%s: repo %q is not owner/name", c.ID, c.Repo)
		}
	}
	for _, id := range []string{"naive", "olcrtc", "qwdtt", "csqtt", "openflux", "mieru", "trusttunnel", "anytls", "tproxy", "mtproxy"} {
		if !seen[id] {
			t.Errorf("the panel's core %s is missing", id)
		}
	}
}
