// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

// The tunnel cores this plugin keeps up to date: the ones whose project publishes a ready program for Linux. The
// other cores of the panel are builds of its own (patched, or built from source), so a release of the project
// is not a drop-in for them and they are not here.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// managedCore is one core the plugin can replace. ID is the panel's name for it (/tunnel/<ID>/...).
type managedCore struct {
	ID   string
	Name string
	Repo string // owner/name on GitHub
	// Asset is the release file to download for a CPU ("amd64", "arm64"); "" when the project has none.
	Asset func(tag, arch string) string
}

var managedCores = []managedCore{
	{ID: "openflux", Name: "OpenFlux", Repo: "p1neappleXpress/OpenFlux", Asset: func(_, arch string) string {
		return "openflux-linux-" + arch
	}},
	{ID: "mieru", Name: "mieru", Repo: "enfein/mieru", Asset: func(tag, arch string) string {
		return "mita_" + strings.TrimPrefix(tag, "v") + "_linux_" + arch + ".tar.gz"
	}},
	{ID: "trusttunnel", Name: "TrustTunnel", Repo: "TrustTunnel/TrustTunnel", Asset: func(tag, arch string) string {
		cpu, ok := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[arch]
		if !ok {
			return ""
		}
		return "trusttunnel-" + tag + "-linux-" + cpu + ".tar.gz"
	}},
}

func findCore(id string) (managedCore, bool) {
	for _, c := range managedCores {
		if c.ID == id {
			return c, true
		}
	}
	return managedCore{}, false
}

// release is what the plugin knows of a project's last release and of the file it would install.
type release struct {
	Tag         string    `json:"tag"`
	PublishedAt time.Time `json:"publishedAt"`
	PageURL     string    `json:"url"`
	AssetName   string    `json:"asset"`
	AssetURL    string    `json:"assetUrl"`
	SHA256      string    `json:"sha256"` // of the file at AssetURL; the panel refuses the download if it differs
}

// upstream finds the last release of a core; the plugin asks GitHub, tests ask a fake.
type upstream interface {
	latest(ctx context.Context, c managedCore, arch string) (release, error)
}

// github asks the public releases API; the plugin needs the network permission for this and nothing else.
type github struct {
	client *http.Client
	base   string // https://api.github.com; tests point it elsewhere
}

func newGithub() *github {
	return &github{client: &http.Client{Timeout: 20 * time.Second}, base: "https://api.github.com"}
}

type ghAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"` // "sha256:<hex>" on releases GitHub has hashed
}

type ghRelease struct {
	Tag         string    `json:"tag_name"`
	PublishedAt time.Time `json:"published_at"`
	URL         string    `json:"html_url"`
	Assets      []ghAsset `json:"assets"`
}

func (g *github) get(ctx context.Context, url, accept string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "better-cores")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func (g *github) latest(ctx context.Context, c managedCore, arch string) (release, error) {
	raw, err := g.get(ctx, g.base+"/repos/"+c.Repo+"/releases/latest", "application/vnd.github+json", 4<<20)
	if err != nil {
		return release{}, err
	}
	var rel ghRelease
	if err := json.Unmarshal(raw, &rel); err != nil {
		return release{}, err
	}
	if rel.Tag == "" {
		return release{}, fmt.Errorf("%s has no release", c.Repo)
	}
	return pickAsset(c, arch, rel, func(url string) ([]byte, error) { return g.get(ctx, url, "text/plain", 1<<20) })
}

// pickAsset chooses the file to install and the SHA-256 it must have. The hash comes from the checksum file the
// project publishes next to its release, when there is one, and from the digest GitHub computed for the file; when
// both exist they must agree, and a release with neither is refused: the panel only installs a file whose hash is known.
func pickAsset(c managedCore, arch string, rel ghRelease, fetch func(url string) ([]byte, error)) (release, error) {
	name := c.Asset(rel.Tag, arch)
	if name == "" {
		return release{}, fmt.Errorf("%s publishes nothing for %s", c.Name, arch)
	}
	var asset *ghAsset
	for i := range rel.Assets {
		if rel.Assets[i].Name == name {
			asset = &rel.Assets[i]
			break
		}
	}
	if asset == nil {
		return release{}, fmt.Errorf("release %s of %s has no %s", rel.Tag, c.Repo, name)
	}
	out := release{Tag: rel.Tag, PublishedAt: rel.PublishedAt, PageURL: rel.URL, AssetName: name, AssetURL: asset.URL}

	digest := strings.ToLower(strings.TrimPrefix(asset.Digest, "sha256:"))
	if asset.Digest != "" && !strings.HasPrefix(asset.Digest, "sha256:") {
		digest = ""
	}
	var fromFile string
	for _, a := range rel.Assets {
		if !isChecksumFileFor(a.Name, name) {
			continue
		}
		body, err := fetch(a.URL)
		if err != nil {
			return release{}, fmt.Errorf("reading %s: %w", a.Name, err)
		}
		if fromFile = checksumOf(body, name); fromFile != "" {
			break
		}
	}
	switch {
	case fromFile != "" && digest != "" && fromFile != digest:
		return release{}, fmt.Errorf("the checksum file and GitHub disagree about %s", name)
	case fromFile != "":
		out.SHA256 = fromFile
	case digest != "":
		out.SHA256 = digest
	default:
		return release{}, fmt.Errorf("no checksum is published for %s", name)
	}
	if !isHex64(out.SHA256) {
		return release{}, fmt.Errorf("the checksum of %s is not a SHA-256", name)
	}
	return out, nil
}

// isChecksumFileFor says whether a release file holds the checksum of asset: <asset>.sha256(.txt) or the usual
// list of all files.
func isChecksumFileFor(file, asset string) bool {
	switch strings.ToLower(file) {
	case strings.ToLower(asset) + ".sha256", strings.ToLower(asset) + ".sha256.txt", "sha256sums", "sha256sums.txt", "checksums.txt":
		return true
	}
	return false
}

// checksumOf finds the SHA-256 of asset in a checksum file: a list of "<hash>  <file>" lines or a lone "<hash>".
func checksumOf(body []byte, asset string) string {
	sc := bufio.NewScanner(strings.NewReader(string(body)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		switch {
		case len(f) == 1 && isHex64(strings.ToLower(f[0])):
			return strings.ToLower(f[0])
		case len(f) >= 2 && strings.TrimPrefix(f[1], "*") == asset && isHex64(strings.ToLower(f[0])):
			return strings.ToLower(f[0])
		}
	}
	return ""
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
