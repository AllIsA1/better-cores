// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	sumA = "709236d17f3104cba07b156e183ebfaa0e708902e35d0c1678e72eac1a64b350"
	sumB = "ea35fdef84bef1378f32b74a2d5a883d508a49dd4e64dba9a3a8363beed5798c"
)

func TestChecksumOfReadsBothLayouts(t *testing.T) {
	list := []byte(sumA + "  openflux-linux-amd64\n" + sumB + " *other\n")
	if got := checksumOf(list, "openflux-linux-amd64"); got != sumA {
		t.Errorf("list: %q", got)
	}
	if got := checksumOf(list, "other"); got != sumB {
		t.Errorf("binary-mode marker: %q", got)
	}
	if got := checksumOf(list, "missing"); got != "" {
		t.Errorf("a file that is not listed has no checksum, got %q", got)
	}
	if got := checksumOf([]byte(strings.ToUpper(sumB)+"  mita.tar.gz\n"), "mita.tar.gz"); got != sumB {
		t.Errorf("upper case: %q", got)
	}
	if got := checksumOf([]byte(sumB+"\n"), "anything"); got != sumB {
		t.Errorf("a file with one hash: %q", got)
	}
	if got := checksumOf([]byte("not a checksum  openflux\n"), "openflux"); got != "" {
		t.Errorf("garbage: %q", got)
	}
}

func mieruCore() managedCore { c, _ := findCore("mieru"); return c }

func TestAssetNamesFollowTheProjects(t *testing.T) {
	for _, c := range []struct {
		core, tag, arch, want string
	}{
		{"openflux", "v0.13.0", "amd64", "openflux-linux-amd64"},
		{"openflux", "v0.13.0", "arm64", "openflux-linux-arm64"},
		{"mieru", "v3.39.0", "amd64", "mita_3.39.0_linux_amd64.tar.gz"},
		{"mieru", "v3.39.0", "arm64", "mita_3.39.0_linux_arm64.tar.gz"},
		{"trusttunnel", "v1.1.0", "amd64", "trusttunnel-v1.1.0-linux-x86_64.tar.gz"},
		{"trusttunnel", "v1.1.0", "arm64", "trusttunnel-v1.1.0-linux-aarch64.tar.gz"},
		{"trusttunnel", "v1.1.0", "riscv64", ""},
	} {
		core, ok := findCore(c.core)
		if !ok {
			t.Fatalf("no core %s", c.core)
		}
		if got := core.Asset(c.tag, c.arch); got != c.want {
			t.Errorf("%s %s %s: %q, want %q", c.core, c.tag, c.arch, got, c.want)
		}
	}
}

func TestPickAssetTakesTheHashFromTheChecksumFileAndGitHub(t *testing.T) {
	const name = "mita_3.39.0_linux_amd64.tar.gz"
	rel := ghRelease{Tag: "v3.39.0", Assets: []ghAsset{
		{Name: name, URL: "https://example.test/" + name, Digest: "sha256:" + sumB},
		{Name: name + ".sha256.txt", URL: "https://example.test/sum"},
	}}
	fetch := func(string) ([]byte, error) { return []byte(sumB + "  " + name + "\n"), nil }
	got, err := pickAsset(mieruCore(), "amd64", rel, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != sumB || got.AssetName != name || got.Tag != "v3.39.0" {
		t.Errorf("got %+v", got)
	}

	// the file alone is enough, and so is GitHub's digest alone
	rel.Assets[0].Digest = ""
	if got, err = pickAsset(mieruCore(), "amd64", rel, fetch); err != nil || got.SHA256 != sumB {
		t.Errorf("file only: %+v %v", got, err)
	}
	rel.Assets = rel.Assets[:1]
	rel.Assets[0].Digest = "sha256:" + sumB
	if got, err = pickAsset(mieruCore(), "amd64", rel, nil); err != nil || got.SHA256 != sumB {
		t.Errorf("digest only: %+v %v", got, err)
	}
}

func TestPickAssetRefusesWhatItCannotVouchFor(t *testing.T) {
	const name = "mita_3.39.0_linux_amd64.tar.gz"
	asset := ghAsset{Name: name, URL: "https://example.test/x"}
	cases := map[string]struct {
		rel   ghRelease
		fetch func(string) ([]byte, error)
		want  string
	}{
		"no checksum anywhere": {ghRelease{Tag: "v3.39.0", Assets: []ghAsset{asset}}, nil, "no checksum"},
		"the two disagree": {ghRelease{Tag: "v3.39.0", Assets: []ghAsset{
			{Name: name, URL: "u", Digest: "sha256:" + sumA}, {Name: name + ".sha256.txt", URL: "s"}}},
			func(string) ([]byte, error) { return []byte(sumB + "  " + name), nil }, "disagree"},
		"the file is missing": {ghRelease{Tag: "v3.40.0", Assets: []ghAsset{asset}}, nil, "has no mita_3.40.0"},
		"a digest of another kind": {ghRelease{Tag: "v3.39.0", Assets: []ghAsset{
			{Name: name, URL: "u", Digest: "sha512:" + sumA}}}, nil, "no checksum"},
		"the checksum file cannot be read": {ghRelease{Tag: "v3.39.0", Assets: []ghAsset{
			asset, {Name: name + ".sha256.txt", URL: "s"}}},
			func(string) ([]byte, error) { return nil, errors.New("boom") }, "boom"},
	}
	for label, c := range cases {
		t.Run(label, func(t *testing.T) {
			_, err := pickAsset(mieruCore(), "amd64", c.rel, c.fetch)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want an error containing %q", err, c.want)
			}
		})
	}
}

func TestGithubReadsTheReleaseOfAProject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/p1neappleXpress/OpenFlux/releases/latest":
			_ = json.NewEncoder(w).Encode(ghRelease{Tag: "v0.14.0", URL: "https://example.test/rel", Assets: []ghAsset{
				{Name: "openflux-linux-amd64", URL: "https://example.test/f", Digest: "sha256:" + sumA}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	g := &github{client: srv.Client(), base: srv.URL}
	openflux, _ := findCore("openflux")
	rel, err := g.latest(context.Background(), openflux, "amd64")
	if err != nil || rel.Tag != "v0.14.0" || rel.SHA256 != sumA || rel.AssetURL != "https://example.test/f" {
		t.Fatalf("got %+v %v", rel, err)
	}
	if _, err := g.latest(context.Background(), mieruCore(), "amd64"); err == nil {
		t.Error("a project GitHub does not know must be an error")
	}
}

// fakeUpstream answers per core; a core missing from the map fails.
type fakeUpstream struct {
	mu   sync.Mutex
	rels map[string]release
	asks int
}

func (f *fakeUpstream) latest(_ context.Context, c managedCore, _ string) (release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asks++
	if r, ok := f.rels[c.ID]; ok {
		return r, nil
	}
	return release{}, errors.New("GitHub does not answer for " + c.ID)
}

// fakePanel records the downloads and answers the version.
type fakePanel struct {
	mu        sync.Mutex
	version   string
	downloads []string
	failWith  error
	gate      chan struct{} // when set, a download waits for it
}

func (f *fakePanel) Do(_ context.Context, method, path string, in, out any) error {
	if path == "/server/status" {
		f.mu.Lock()
		v := f.version
		f.mu.Unlock()
		if v == "" {
			return errors.New("panel unreachable")
		}
		raw, _ := json.Marshal(map[string]string{"panelVersion": v})
		return json.Unmarshal(raw, out)
	}
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	body := in.(map[string]string)
	f.downloads = append(f.downloads, method+" "+path+" "+body["url"]+" "+body["sha256"])
	return f.failWith
}

func (f *fakePanel) setVersion(v string) { f.mu.Lock(); f.version = v; f.mu.Unlock() }

func rel(tag string) release {
	return release{Tag: tag, AssetName: "a-" + tag, AssetURL: "https://example.test/" + tag, SHA256: sumA}
}

func newTestUpdater(t *testing.T, up *fakeUpstream, p *fakePanel) *updater {
	t.Helper()
	return newUpdater(p, p, up, "amd64", filepath.Join(t.TempDir(), "cores.json"))
}

func waitIdle(t *testing.T, u *updater, id string) {
	t.Helper()
	for i := 0; i < 500; i++ {
		u.mu.Lock()
		busy := u.busy[id]
		u.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s is still being installed", id)
}

func TestCheckRecordsTheReleasesAndSurvivesARestart(t *testing.T) {
	up := &fakeUpstream{rels: map[string]release{"openflux": rel("v0.13.0"), "mieru": rel("v3.39.0")}} // trusttunnel fails
	u := newTestUpdater(t, up, &fakePanel{version: "p1"})
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	u.now = func() time.Time { return now }
	if err := u.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := u.view()
	if !v.CheckedAt.Equal(now) || !v.NextCheckAt.Equal(now.Add(24*time.Hour)) {
		t.Errorf("checked %v, next %v", v.CheckedAt, v.NextCheckAt)
	}
	byID := map[string]coreView{}
	for _, c := range v.Cores {
		byID[c.ID] = c
	}
	if byID["openflux"].Latest == nil || byID["openflux"].Latest.Tag != "v0.13.0" {
		t.Errorf("openflux: %+v", byID["openflux"])
	}
	if byID["trusttunnel"].Latest != nil || !strings.Contains(byID["trusttunnel"].Error, "does not answer") {
		t.Errorf("a project that fails shows the reason and no release: %+v", byID["trusttunnel"])
	}
	if byID["openflux"].UpdateAvailable {
		t.Error("nothing is installed, so nothing is an update")
	}

	again := newUpdater(&fakePanel{}, &fakePanel{}, up, "amd64", u.path)
	if got := again.view().Cores[0].Latest; got == nil || got.Tag != "v0.13.0" {
		t.Errorf("the releases are lost on restart: %+v", got)
	}
}

func TestACheckThatReachesNobodyIsRetriedSoon(t *testing.T) {
	up := &fakeUpstream{rels: map[string]release{}}
	u := newTestUpdater(t, up, &fakePanel{})
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	u.now = func() time.Time { return now }
	if !u.due() {
		t.Error("a plugin that never looked is due")
	}
	if err := u.check(context.Background()); err == nil {
		t.Fatal("a look that reached nobody is an error")
	}
	if !u.view().CheckedAt.IsZero() {
		t.Error("a failed look is not a look")
	}
	now = now.Add(30 * time.Minute)
	if u.due() {
		t.Error("tried half an hour ago: wait")
	}
	now = now.Add(31 * time.Minute)
	if !u.due() {
		t.Error("an hour after a failure it is due again")
	}
	up.rels["openflux"] = rel("v1")
	if err := u.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(23 * time.Hour)
	if u.due() {
		t.Error("looked a moment ago: not due for a day")
	}
	now = now.Add(2 * time.Hour)
	if !u.due() {
		t.Error("a day later it is due")
	}
}

func TestInstallHasThePanelDownloadAndRemembersIt(t *testing.T) {
	up := &fakeUpstream{rels: map[string]release{"mieru": rel("v3.39.0")}}
	p := &fakePanel{version: "lucx.329"}
	u := newTestUpdater(t, up, p)
	if err := u.install("anytls"); !errors.Is(err, errUnknownCore) {
		t.Errorf("a core the plugin does not manage: %v", err)
	}
	if err := u.install("mieru"); err == nil || !strings.Contains(err.Error(), "look for updates first") {
		t.Errorf("no release known yet: %v", err)
	}
	_ = u.check(context.Background())
	if err := u.install("mieru"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, u, "mieru")
	if len(p.downloads) != 1 || p.downloads[0] != "POST /tunnel/mieru/download https://example.test/v3.39.0 "+sumA {
		t.Fatalf("downloads: %v", p.downloads)
	}
	var mieru coreView
	for _, c := range u.view().Cores {
		if c.ID == "mieru" {
			mieru = c
		}
	}
	if mieru.Installed == nil || mieru.Installed.Tag != "v3.39.0" || mieru.Installed.PanelVersion != "lucx.329" || mieru.UpdateAvailable || mieru.Error != "" {
		t.Errorf("after the install: %+v", mieru)
	}

	// a newer release makes it an update
	up.rels["mieru"] = rel("v3.40.0")
	_ = u.check(context.Background())
	for _, c := range u.view().Cores {
		if c.ID == "mieru" && !c.UpdateAvailable {
			t.Error("v3.40.0 after v3.39.0 is an update")
		}
	}
	// and the install survives a restart
	again := newUpdater(p, p, up, "amd64", u.path)
	if in := again.st.Installed["mieru"]; in.Tag != "v3.39.0" {
		t.Errorf("the install is lost on restart: %+v", in)
	}
}

func TestAFailedInstallShowsWhyAndRecordsNothing(t *testing.T) {
	up := &fakeUpstream{rels: map[string]release{"mieru": rel("v3.39.0")}}
	p := &fakePanel{version: "p", failWith: errors.New("tunnel: sha256 mismatch")}
	u := newTestUpdater(t, up, p)
	_ = u.check(context.Background())
	if err := u.install("mieru"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, u, "mieru")
	for _, c := range u.view().Cores {
		if c.ID == "mieru" && (c.Installed != nil || !strings.Contains(c.Error, "sha256 mismatch") || c.Installing) {
			t.Errorf("after a failure: %+v", c)
		}
	}
}

func TestACoreIsInstalledOneAtATime(t *testing.T) {
	up := &fakeUpstream{rels: map[string]release{"mieru": rel("v3.39.0")}}
	p := &fakePanel{version: "p", gate: make(chan struct{})}
	u := newTestUpdater(t, up, p)
	_ = u.check(context.Background())
	if err := u.install("mieru"); err != nil {
		t.Fatal(err)
	}
	if err := u.install("mieru"); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("the second install while the first runs: %v", err)
	}
	close(p.gate)
	waitIdle(t, u, "mieru")
}

func TestACorePutBackAfterAPanelUpdate(t *testing.T) {
	up := &fakeUpstream{rels: map[string]release{"mieru": rel("v3.39.0"), "openflux": rel("v0.13.0")}}
	p := &fakePanel{version: "lucx.329"}
	u := newTestUpdater(t, up, p)
	_ = u.check(context.Background())
	_ = u.install("mieru")
	waitIdle(t, u, "mieru")
	p.downloads = nil

	u.reapplyAfterPanelUpdate(context.Background())
	if len(p.downloads) != 0 {
		t.Errorf("the same panel: nothing to put back, got %v", p.downloads)
	}

	// the panel is updated, and a newer release has come out meanwhile: the OLD release goes back, not the new one
	p.setVersion("lucx.330")
	up.rels["mieru"] = rel("v3.40.0")
	_ = u.check(context.Background())
	u.reapplyAfterPanelUpdate(context.Background())
	if len(p.downloads) != 1 || !strings.Contains(p.downloads[0], "v3.39.0") {
		t.Fatalf("downloads: %v", p.downloads)
	}
	if got := u.st.Installed["mieru"]; got.Tag != "v3.39.0" || got.PanelVersion != "lucx.330" {
		t.Errorf("installed: %+v", got)
	}
	u.reapplyAfterPanelUpdate(context.Background())
	if len(p.downloads) != 1 {
		t.Errorf("put back once per panel update, got %v", p.downloads)
	}
}

func TestAPanelThatDoesNotSayItsVersionIsNotReinstalledInto(t *testing.T) {
	up := &fakeUpstream{rels: map[string]release{"mieru": rel("v3.39.0")}}
	p := &fakePanel{} // no version
	u := newTestUpdater(t, up, p)
	_ = u.check(context.Background())
	_ = u.install("mieru")
	waitIdle(t, u, "mieru")
	p.downloads = nil
	u.reapplyAfterPanelUpdate(context.Background())
	p.setVersion("lucx.329")
	u.reapplyAfterPanelUpdate(context.Background())
	if len(p.downloads) != 0 {
		t.Errorf("a version learnt late is adopted, not a reason to reinstall: %v", p.downloads)
	}
	if got := u.st.Installed["mieru"].PanelVersion; got != "lucx.329" {
		t.Errorf("version %q", got)
	}
}

func TestCoresPageAnswersOnlyThePanel(t *testing.T) {
	up := &fakeUpstream{rels: map[string]release{"mieru": rel("v3.39.0")}}
	p := &fakePanel{version: "p"}
	u := newTestUpdater(t, up, p)
	srv := httptest.NewServer(newMux(newJobBoard(), u))
	defer srv.Close()

	do := func(method, path, user string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		if user != "" {
			req.Header.Set("X-LucX-User", user)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}
	if code, _ := do("GET", "/api/cores", ""); code != http.StatusForbidden {
		t.Errorf("no panel user: %d", code)
	}
	if code, _ := do("POST", "/api/cores/mieru/install", ""); code != http.StatusForbidden {
		t.Errorf("no panel user: %d", code)
	}
	if code, body := do("GET", "/api/cores", "admin"); code != 200 || body["success"] != true ||
		len(body["obj"].(map[string]any)["cores"].([]any)) != len(managedCores) {
		t.Errorf("list: %d %v", code, body)
	}
	if code, _ := do("POST", "/api/cores/mieru/install", "admin"); code != http.StatusConflict {
		t.Errorf("install before any look: %d", code)
	}
	if code, body := do("POST", "/api/cores/check", "admin"); code != 200 || body["obj"].(map[string]any)["checkedAt"] == nil {
		t.Errorf("check: %d %v", code, body)
	}
	if code, _ := do("POST", "/api/cores/nope/install", "admin"); code != http.StatusNotFound {
		t.Errorf("unknown core: %d", code)
	}
	if code, _ := do("POST", "/api/cores/mieru/install", "admin"); code != http.StatusAccepted {
		t.Errorf("install: %d", code)
	}
	waitIdle(t, u, "mieru")
	if code, _ := do("DELETE", "/api/cores", "admin"); code == http.StatusOK {
		t.Errorf("DELETE answered 200")
	}
	if code, _ := do("GET", "/api/other", "admin"); code == http.StatusOK {
		t.Errorf("an unknown path answered 200")
	}
}

func TestACheckDoesNotRunTwiceAtOnce(t *testing.T) {
	gate := make(chan struct{})
	up := &blockingUpstream{gate: gate}
	u := newUpdater(&fakePanel{}, &fakePanel{}, up, "amd64", "")
	done := make(chan error, 1)
	go func() { done <- u.check(context.Background()) }()
	for i := 0; i < 500 && !u.view().Checking; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if err := u.check(context.Background()); !errors.Is(err, errCheckRunning) {
		t.Errorf("second check: %v", err)
	}
	close(gate)
	<-done
}

type blockingUpstream struct{ gate chan struct{} }

func (b *blockingUpstream) latest(_ context.Context, _ managedCore, _ string) (release, error) {
	<-b.gate
	return rel("v1"), nil
}
