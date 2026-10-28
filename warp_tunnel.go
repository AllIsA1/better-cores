// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

// The WARP engine's tunnel: an AmneziaWG device in this process, on a user-space network stack, as a client of a
// Cloudflare WARP endpoint. It touches no interface and no route of the machine; it is brought up to look at what the
// account and the endpoint do (handshake time, the exit's trace, speed) and taken down again.

package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/netstack"
)

// cloudflareWarpKey is the public key of Cloudflare's WARP endpoints; an account's answer names it too.
const cloudflareWarpKey = "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="

// warpAccount is a registered WARP device as the tunnel needs it.
type warpAccount struct {
	PrivateKey    string   `json:"privateKey"`              // base64
	Address       []string `json:"address"`                 // the device's addresses, "172.16.0.2/32", "2606:4700:110:...::/128"
	PeerPublicKey string   `json:"peerPublicKey,omitempty"` // base64; Cloudflare's key when empty
}

// awgParams are the AmneziaWG obfuscation settings. For WARP only the junk packets count: the server speaks plain
// WireGuard, so S1/S2 and H1-H4 stay at their defaults (0 and 1-4).
type awgParams struct {
	Jc   int    `json:"jc"`
	Jmin int    `json:"jmin"`
	Jmax int    `json:"jmax"`
	S1   int    `json:"s1,omitempty"`
	S2   int    `json:"s2,omitempty"`
	H1   string `json:"h1,omitempty"`
	H2   string `json:"h2,omitempty"`
	H3   string `json:"h3,omitempty"`
	H4   string `json:"h4,omitempty"`
	I1   string `json:"i1,omitempty"`
	I2   string `json:"i2,omitempty"`
	I3   string `json:"i3,omitempty"`
	I4   string `json:"i4,omitempty"`
	I5   string `json:"i5,omitempty"`
}

// tunnelParams is everything one tunnel needs.
type tunnelParams struct {
	Account  warpAccount `json:"account"`
	Endpoint string      `json:"endpoint"` // host:port
	MTU      int         `json:"mtu,omitempty"`
	AWG      awgParams   `json:"awg"`
}

func keyHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(raw) != 32 {
		return "", errors.New("a key must be 32 bytes in base64")
	}
	return hex.EncodeToString(raw), nil
}

func keyBytes(b64 string) ([32]byte, error) {
	var out [32]byte
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(raw) != 32 {
		return out, errors.New("a key must be 32 bytes in base64")
	}
	copy(out[:], raw)
	return out, nil
}

func (a warpAccount) peerKey() string {
	if strings.TrimSpace(a.PeerPublicKey) != "" {
		return strings.TrimSpace(a.PeerPublicKey)
	}
	return cloudflareWarpKey
}

func (a warpAccount) validate() error {
	if _, err := keyBytes(a.PrivateKey); err != nil {
		return fmt.Errorf("account private key: %w", err)
	}
	if _, err := keyBytes(a.peerKey()); err != nil {
		return fmt.Errorf("account peer key: %w", err)
	}
	if len(a.addresses()) == 0 {
		return errors.New("the account has no address")
	}
	return nil
}

// addresses are the device's addresses without their masks.
func (a warpAccount) addresses() []netip.Addr {
	var out []netip.Addr
	for _, s := range a.Address {
		s = strings.TrimSpace(s)
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p.Addr())
		} else if ip, err := netip.ParseAddr(s); err == nil {
			out = append(out, ip)
		}
	}
	return out
}

// uapi renders the device configuration in the form the device takes (hex keys, one setting per line).
func (p tunnelParams) uapi(endpoint string) (string, error) {
	priv, err := keyHex(p.Account.PrivateKey)
	if err != nil {
		return "", err
	}
	peer, err := keyHex(p.Account.peerKey())
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\nreplace_peers=true\n", priv)
	o := p.AWG
	fmt.Fprintf(&b, "jc=%d\njmin=%d\njmax=%d\ns1=%d\ns2=%d\n", o.Jc, o.Jmin, o.Jmax, o.S1, o.S2)
	for k, v := range map[string]string{"h1": o.H1, "h2": o.H2, "h3": o.H3, "h4": o.H4, "i1": o.I1, "i2": o.I2, "i3": o.I3, "i4": o.I4, "i5": o.I5} {
		if strings.TrimSpace(v) != "" && !strings.ContainsAny(v, "\r\n") {
			fmt.Fprintf(&b, "%s=%s\n", k, strings.TrimSpace(v))
		}
	}
	fmt.Fprintf(&b, "public_key=%s\nendpoint=%s\npersistent_keepalive_interval=25\nallowed_ip=0.0.0.0/0\nallowed_ip=::/0\n", peer, endpoint)
	return b.String(), nil
}

// warpTunnel is a device and its network stack.
type warpTunnel struct {
	dev *device.Device
	net *netstack.Net
}

var warpDNS = []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("1.0.0.1")}

// openTunnel brings a tunnel up to endpoint (an IP:port or host:port). The caller closes it.
func openTunnel(p tunnelParams, endpoint string) (*warpTunnel, error) {
	if err := p.Account.validate(); err != nil {
		return nil, err
	}
	mtu := p.MTU
	if mtu <= 0 {
		mtu = 1280
	}
	tun, tnet, err := netstack.CreateNetTUN(p.Account.addresses(), warpDNS, mtu)
	if err != nil {
		return nil, fmt.Errorf("network stack: %w", err)
	}
	dev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	conf, err := p.uapi(endpoint)
	if err == nil {
		err = dev.IpcSet(conf)
	}
	if err == nil {
		err = dev.Up()
	}
	if err != nil {
		dev.Close()
		return nil, err
	}
	return &warpTunnel{dev: dev, net: tnet}, nil
}

func (t *warpTunnel) Close() { t.dev.Close() }

// lastHandshake is when the peer last completed a handshake, zero if it never did.
func (t *warpTunnel) lastHandshake() time.Time {
	dump, err := t.dev.IpcGet()
	if err != nil {
		return time.Time{}
	}
	var sec, nsec int64
	sc := bufio.NewScanner(strings.NewReader(dump))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "last_handshake_time_sec":
			sec, _ = strconv.ParseInt(v, 10, 64)
		case "last_handshake_time_nsec":
			nsec, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if sec == 0 && nsec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, nsec)
}

// waitHandshake makes the device talk (a dial towards the exit does that) and waits for the peer's answer. It returns
// how long that took.
func (t *warpTunnel) waitHandshake(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	kick, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { // a connection attempt is what makes the device send its initiation, and send it again
		for kick.Err() == nil {
			dctx, c := context.WithTimeout(kick, 2*time.Second)
			if conn, err := t.net.DialContext(dctx, "tcp", "1.1.1.1:443"); err == nil {
				conn.Close()
			}
			c()
		}
	}()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if hs := t.lastHandshake(); !hs.IsZero() && !hs.Before(start.Add(-time.Second)) {
			return time.Since(start), nil
		}
		select {
		case <-ctx.Done():
			return 0, errors.New("no handshake: the endpoint did not answer")
		case <-tick.C:
		}
	}
}

// checkResult is what a check found out.
type checkResult struct {
	OK          bool    `json:"ok"`
	Error       string  `json:"error,omitempty"`
	Endpoint    string  `json:"endpoint"`
	HandshakeMs int     `json:"handshakeMs,omitempty"`
	Warp        string  `json:"warp,omitempty"` // off, on or plus, as the exit's trace says
	IP          string  `json:"ip,omitempty"`
	Colo        string  `json:"colo,omitempty"`
	Loc         string  `json:"loc,omitempty"`
	LatencyMs   int     `json:"latencyMs,omitempty"` // an HTTPS request through the tunnel
	Bytes       int64   `json:"bytes,omitempty"`
	DownMbps    float64 `json:"downMbps,omitempty"`
}

// parseTrace reads Cloudflare's /cdn-cgi/trace (key=value lines).
func parseTrace(r io.Reader) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(io.LimitReader(r, 64<<10))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			out[k] = v
		}
	}
	return out
}

// httpClient dials through the tunnel only.
func (t *warpTunnel) httpClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{DialContext: t.net.DialContext, DisableKeepAlives: false, ForceAttemptHTTP2: false},
	}
}

// trace asks the exit who it is.
func (t *warpTunnel) trace(ctx context.Context) (map[string]string, time.Duration, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.cloudflare.com/cdn-cgi/trace", nil)
	start := time.Now()
	resp, err := t.httpClient(12 * time.Second).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("trace answered HTTP %d", resp.StatusCode)
	}
	return parseTrace(resp.Body), time.Since(start), nil
}

// speedURL serves as many bytes as asked, from Cloudflare's own speed test.
const speedURL = "https://speed.cloudflare.com/__down?bytes="

// download reads through the tunnel from several streams at once for at most d, and returns the bytes and the rate.
func (t *warpTunnel) download(ctx context.Context, d time.Duration, streams int) (int64, float64) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	var total atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, speedURL+"50000000", nil)
			resp, err := t.httpClient(d + 2*time.Second).Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			buf := make([]byte, 64<<10)
			for {
				n, err := resp.Body.Read(buf)
				total.Add(int64(n))
				if err != nil {
					return
				}
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()
	n := total.Load()
	if elapsed <= 0 || n == 0 {
		return n, 0
	}
	return n, float64(n) * 8 / elapsed / 1e6
}

// runCheck brings the tunnel up to p.Endpoint and looks: handshake, trace, and when speed is set a download.
func runCheck(ctx context.Context, p tunnelParams, speed bool) checkResult {
	res := checkResult{Endpoint: p.Endpoint}
	if err := checkEndpoint(p.Endpoint); err != nil {
		res.Error = err.Error()
		return res
	}
	tun, err := openTunnel(p, p.Endpoint)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer tun.Close()

	hctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	hs, err := tun.waitHandshake(hctx)
	cancel()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.HandshakeMs = int(hs.Milliseconds())

	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	trace, took, err := tun.trace(tctx)
	cancel()
	if err != nil {
		res.Error = "the tunnel is up but the exit does not answer: " + err.Error()
		return res
	}
	res.LatencyMs = int(took.Milliseconds())
	res.Warp, res.IP, res.Colo, res.Loc = trace["warp"], trace["ip"], trace["colo"], trace["loc"]
	res.OK = res.Warp == "on" || res.Warp == "plus"
	if !res.OK {
		res.Error = "the exit answers but this is not WARP (warp=" + res.Warp + ")"
		return res
	}
	if speed {
		res.Bytes, res.DownMbps = tun.download(ctx, 7*time.Second, 4)
		res.DownMbps = float64(int(res.DownMbps*10)) / 10
	}
	return res
}
