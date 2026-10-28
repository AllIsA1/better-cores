// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

// Keeps the cores of the panel up to date: looks for new releases once a day (and when asked), installs one when
// told to, and puts it back after a panel update, which brings its own copy of every core.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	checkEvery   = 24 * time.Hour // how old the last look at the releases may get
	retryAfter   = time.Hour      // how long to wait after a look that found nothing at all
	tick         = 10 * time.Minute
	startupDelay = 30 * time.Second // let the panel settle before the first call
)

// installed is a core this plugin put in place of the panel's own.
type installed struct {
	release
	At           time.Time `json:"at"`
	PanelVersion string    `json:"panelVersion,omitempty"` // the panel it was installed into: another one brought its own copy
}

// stored is what survives a restart.
type stored struct {
	CheckedAt   time.Time            `json:"checkedAt"`   // the last look that reached at least one project
	AttemptedAt time.Time            `json:"attemptedAt"` // the last look at all
	Latest      map[string]release   `json:"latest"`
	Installed   map[string]installed `json:"installed"`
}

type panelAPI interface {
	Do(ctx context.Context, method, path string, in, out any) error
}

type updater struct {
	api  panelAPI // quick calls
	slow panelAPI // the panel's download of a core
	up   upstream
	arch string
	path string
	now  func() time.Time

	mu       sync.Mutex
	st       stored
	checking bool
	busy     map[string]bool   // cores being installed
	errs     map[string]string // the last failure per core (look-up or install)
}

func newUpdater(api, slow panelAPI, up upstream, arch, path string) *updater {
	u := &updater{api: api, slow: slow, up: up, arch: arch, path: path, now: time.Now,
		busy: map[string]bool{}, errs: map[string]string{}}
	u.st = stored{Latest: map[string]release{}, Installed: map[string]installed{}}
	if raw, err := os.ReadFile(path); err == nil {
		var st stored
		if json.Unmarshal(raw, &st) == nil {
			u.st = st
		}
	}
	if u.st.Latest == nil {
		u.st.Latest = map[string]release{}
	}
	if u.st.Installed == nil {
		u.st.Installed = map[string]installed{}
	}
	return u
}

// save writes the state next to itself and moves it into place, so a crash never leaves half a file. Callers hold mu.
func (u *updater) save() {
	if u.path == "" {
		return
	}
	raw, err := json.MarshalIndent(u.st, "", "  ")
	if err == nil {
		tmp := u.path + ".tmp"
		if err = os.WriteFile(tmp, raw, 0o600); err == nil {
			err = os.Rename(tmp, u.path)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "better-cores: cannot save its state:", err)
	}
}

// coreView is what the panel's Cores page shows for one core.
type coreView struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Repo            string     `json:"repo"`
	Latest          *release   `json:"latest,omitempty"`
	Installed       *installed `json:"installed,omitempty"`
	UpdateAvailable bool       `json:"updateAvailable"`
	Installing      bool       `json:"installing"`
	Error           string     `json:"error,omitempty"`
}

type view struct {
	CheckedAt   time.Time  `json:"checkedAt"`
	NextCheckAt time.Time  `json:"nextCheckAt"`
	Checking    bool       `json:"checking"`
	Cores       []coreView `json:"cores"`
}

func (u *updater) view() view {
	u.mu.Lock()
	defer u.mu.Unlock()
	v := view{CheckedAt: u.st.CheckedAt, Checking: u.checking}
	v.NextCheckAt = u.nextCheck()
	for _, c := range managedCores {
		cv := coreView{ID: c.ID, Name: c.Name, Repo: c.Repo, Installing: u.busy[c.ID], Error: u.errs[c.ID]}
		if r, ok := u.st.Latest[c.ID]; ok {
			cv.Latest = &r
		}
		if in, ok := u.st.Installed[c.ID]; ok {
			cv.Installed = &in
			cv.UpdateAvailable = cv.Latest != nil && cv.Latest.Tag != in.Tag
		}
		v.Cores = append(v.Cores, cv)
	}
	return v
}

// nextCheck is when the daily look is due. Callers hold mu.
func (u *updater) nextCheck() time.Time {
	if u.st.AttemptedAt.After(u.st.CheckedAt) {
		// the last look reached nobody: try again soon, not tomorrow
		return u.st.AttemptedAt.Add(retryAfter)
	}
	return u.st.CheckedAt.Add(checkEvery)
}

var errCheckRunning = errors.New("a check is already running")

// check looks at every project's last release. One failing project does not stop the others.
func (u *updater) check(ctx context.Context) error {
	u.mu.Lock()
	if u.checking {
		u.mu.Unlock()
		return errCheckRunning
	}
	u.checking = true
	u.mu.Unlock()

	found := map[string]release{}
	failed := map[string]string{}
	var wg sync.WaitGroup
	var resMu sync.Mutex
	for _, c := range managedCores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
			defer cancel()
			rel, err := u.up.latest(cctx, c, u.arch)
			resMu.Lock()
			defer resMu.Unlock()
			if err != nil {
				failed[c.ID] = err.Error()
				return
			}
			found[c.ID] = rel
		}()
	}
	wg.Wait()

	u.mu.Lock()
	defer u.mu.Unlock()
	u.checking = false
	u.st.AttemptedAt = u.now()
	for id, rel := range found {
		u.st.Latest[id] = rel
		if u.busy[id] {
			continue
		}
		delete(u.errs, id)
	}
	for id, msg := range failed {
		if !u.busy[id] {
			u.errs[id] = msg
		}
	}
	if len(found) > 0 {
		u.st.CheckedAt = u.st.AttemptedAt
	}
	u.save()
	if len(found) == 0 {
		return fmt.Errorf("no release could be read: %s", firstOf(failed))
	}
	return nil
}

func firstOf(m map[string]string) string {
	for _, v := range m {
		return v
	}
	return ""
}

// panelVersion asks the panel which version it is; "" when it cannot say.
func (u *updater) panelVersion(ctx context.Context) string {
	var st struct {
		PanelVersion string `json:"panelVersion"`
	}
	if err := u.api.Do(ctx, http.MethodGet, "/server/status", nil, &st); err != nil {
		return ""
	}
	return st.PanelVersion
}

var errUnknownCore = errors.New("this plugin does not manage that core")

// install starts putting the last release of a core in place of the panel's own. It returns at once; the page
// reads the outcome from view.
func (u *updater) install(id string) error {
	if _, ok := findCore(id); !ok {
		return errUnknownCore
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	rel, ok := u.st.Latest[id]
	if !ok {
		return errors.New("look for updates first: no release is known yet")
	}
	if u.busy[id] {
		return errors.New("this core is being installed already")
	}
	u.busy[id] = true
	delete(u.errs, id)
	go u.apply(id, rel)
	return nil
}

// apply has the panel download the file (the panel checks the hash and unpacks the archive) and records it.
func (u *updater) apply(id string, rel release) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	err := u.slow.Do(ctx, http.MethodPost, "/tunnel/"+id+"/download",
		map[string]string{"url": rel.AssetURL, "sha256": rel.SHA256}, nil)
	version := u.panelVersion(ctx)

	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.busy, id)
	if err != nil {
		u.errs[id] = err.Error()
		return
	}
	u.st.Installed[id] = installed{release: rel, At: u.now(), PanelVersion: version}
	u.save()
}

// reapplyAfterPanelUpdate puts the cores back that a panel update replaced: the new panel brings its own copy of each,
// and an operator who chose a release of their own expects it to stay. The same release is installed again, never a newer one.
func (u *updater) reapplyAfterPanelUpdate(ctx context.Context) {
	u.mu.Lock()
	if len(u.st.Installed) == 0 {
		u.mu.Unlock()
		return
	}
	u.mu.Unlock()
	version := u.panelVersion(ctx)
	if version == "" {
		return
	}
	u.mu.Lock()
	var todo []installed
	var ids []string
	for id, in := range u.st.Installed {
		switch {
		case in.PanelVersion == version || u.busy[id]:
		case in.PanelVersion == "":
			// installed while the panel did not say what it was: nothing to compare, so trust it from now on
			in.PanelVersion = version
			u.st.Installed[id] = in
			u.save()
		default:
			u.busy[id] = true
			todo, ids = append(todo, in), append(ids, id)
		}
	}
	u.mu.Unlock()
	for i, in := range todo {
		u.reinstall(ids[i], in.release, version)
	}
}

func (u *updater) reinstall(id string, rel release, version string) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	err := u.slow.Do(ctx, http.MethodPost, "/tunnel/"+id+"/download",
		map[string]string{"url": rel.AssetURL, "sha256": rel.SHA256}, nil)
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.busy, id)
	if err != nil {
		u.errs[id] = "putting " + rel.Tag + " back after the panel update failed: " + err.Error()
		return
	}
	in := u.st.Installed[id]
	in.PanelVersion = version
	u.st.Installed[id] = in
	u.save()
}

// due says whether the daily look is overdue.
func (u *updater) due() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return !u.checking && !u.now().Before(u.nextCheck())
}

// run is the plugin's clock: after a short wait, and then every few minutes, it looks at the releases when a day has
// passed and checks that the cores it installed are still the panel's.
func (u *updater) run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(startupDelay):
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if u.due() {
			if err := u.check(ctx); err != nil && !errors.Is(err, errCheckRunning) {
				fmt.Fprintln(os.Stderr, "better-cores:", err)
			}
		}
		u.reapplyAfterPanelUpdate(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func statePath() string {
	if dir := StateDir(); dir != "" {
		return filepath.Join(dir, "cores.json")
	}
	return ""
}
