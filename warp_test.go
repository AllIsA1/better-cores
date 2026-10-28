// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/netstack"
	"golang.org/x/crypto/curve25519"
)

func newKey(t *testing.T) (priv, pub [32]byte) {
	t.Helper()
	if _, err := cryptorand.Read(priv[:]); err != nil {
		t.Fatal(err)
	}
	p, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	copy(pub[:], p)
	return priv, pub
}

func b64(k [32]byte) string { return base64.StdEncoding.EncodeToString(k[:]) }

// stands in for a WARP endpoint: a plain WireGuard peer (no obfuscation settings) on a loopback UDP port that knows one
// device key.
func startEndpoint(t *testing.T, endpointPriv, devicePub [32]byte) (port int) {
	t.Helper()
	l, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	port = l.LocalAddr().(*net.UDPAddr).Port
	l.Close()
	tun, _, err := netstack.CreateNetTUN([]netip.Addr{netip.MustParseAddr("172.16.0.1")}, nil, 1280)
	if err != nil {
		t.Fatal(err)
	}
	dev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	t.Cleanup(dev.Close)
	conf := fmt.Sprintf("private_key=%s\nlisten_port=%d\npublic_key=%s\nallowed_ip=0.0.0.0/0\n",
		hex.EncodeToString(endpointPriv[:]), port, hex.EncodeToString(devicePub[:]))
	if err := dev.IpcSet(conf); err != nil {
		t.Fatal(err)
	}
	if err := dev.Up(); err != nil {
		t.Fatal(err)
	}
	return port
}

func TestHandshakeProbeIsAnsweredByARealWireGuardPeerAndOnlyForAKnownKey(t *testing.T) {
	devPriv, devPub := newKey(t)
	epPriv, epPub := newKey(t)
	port := startEndpoint(t, epPriv, devPub)
	addr := netip.MustParseAddrPort(fmt.Sprintf("127.0.0.1:%d", port))
	ctx := context.Background()

	rtt, err := probe(ctx, devPriv, epPub, awgParams{}, addr)
	if err != nil {
		t.Fatalf("a registered device got no answer: %v", err)
	}
	if rtt <= 0 || rtt > time.Second {
		t.Errorf("rtt = %v", rtt)
	}
	// With junk packets first, as an AmneziaWG client sends them: the endpoint drops those and still answers.
	time.Sleep(handshakeSpacing)
	if _, err := probe(ctx, devPriv, epPub, awgParams{Jc: 4, Jmin: 40, Jmax: 70}, addr); err != nil {
		t.Fatalf("junk before the handshake broke it: %v", err)
	}
	// An unknown key gets no answer: that is how an endpoint that is not Cloudflare's, or an unregistered device, shows.
	otherPriv, _ := newKey(t)
	short, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	if _, err := probe(short, otherPriv, epPub, awgParams{}, addr); err == nil {
		t.Error("an unknown device was answered")
	}
	// And a wrong peer key is not answered either: the MAC does not fit.
	_, wrongPub := newKey(t)
	short2, cancel2 := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel2()
	if _, err := probe(short2, devPriv, wrongPub, awgParams{}, addr); err == nil {
		t.Error("a handshake for another peer key was answered")
	}
}

func TestIsAnswerAcceptsOnlyTheAnswerToThisInitiation(t *testing.T) {
	resp := make([]byte, wgResponseSize)
	resp[0] = 2
	resp[8], resp[9], resp[10], resp[11] = 7, 0, 0, 0
	if isAnswer(resp, 7) != nil {
		t.Error("the response to sender 7 was refused")
	}
	if isAnswer(resp, 8) == nil {
		t.Error("the response to another sender was accepted")
	}
	if isAnswer(resp[:50], 7) == nil || isAnswer(nil, 7) == nil {
		t.Error("a short packet was accepted")
	}
	junk := make([]byte, wgResponseSize)
	junk[0] = 0xff
	if isAnswer(junk, 7) == nil {
		t.Error("junk was accepted")
	}
	cookie := make([]byte, wgCookieSize)
	cookie[0] = 3
	cookie[4] = 7
	if isAnswer(cookie, 7) != nil {
		t.Error("a cookie reply to this initiation was refused")
	}
}

func TestTunnelHandshakesWithAPlainPeerThoughItSendsJunk(t *testing.T) {
	devPriv, devPub := newKey(t)
	epPriv, epPub := newKey(t)
	port := startEndpoint(t, epPriv, devPub)
	p := tunnelParams{
		Account: warpAccount{PrivateKey: b64(devPriv), PeerPublicKey: b64(epPub), Address: []string{"172.16.0.2/32", "2606:4700:110:8a36::2/128"}},
		AWG:     awgParams{Jc: 4, Jmin: 40, Jmax: 70},
	}
	tun, err := openTunnel(p, fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	took, err := tun.waitHandshake(ctx)
	if err != nil {
		t.Fatalf("the tunnel did not handshake: %v", err)
	}
	if took <= 0 || took > 6*time.Second {
		t.Errorf("handshake took %v", took)
	}
}

func TestTunnelToAnEndpointThatNeverAnswersFailsInsteadOfHanging(t *testing.T) {
	devPriv, _ := newKey(t)
	_, epPub := newKey(t)
	l, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}) // listens and says nothing
	defer l.Close()
	p := tunnelParams{Account: warpAccount{PrivateKey: b64(devPriv), PeerPublicKey: b64(epPub), Address: []string{"172.16.0.2/32"}}}
	tun, err := openTunnel(p, l.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	if _, err := tun.waitHandshake(ctx); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("err = %v", err)
	}
}

func TestOnlyWarpEndpointsAreAccepted(t *testing.T) {
	for _, ok := range []string{"engage.cloudflareclient.com:2408", "ENGAGE.cloudflareclient.com:500", "162.159.192.1:2408", "188.114.97.66:1701", "[2606:4700:d0::a29f:c001]:2408"} {
		if err := checkEndpoint(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "example.com:2408", "8.8.8.8:2408", "127.0.0.1:2408", "162.159.192.1:53", "162.159.192.1", "162.159.192.1:0", "10.0.0.1:2408",
		"engage.cloudflareclient.com.evil.example:2408", "162.159.200.1:2408", "[::1]:2408"} {
		if checkEndpoint(bad) == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestCandidatesStayInsideTheWarpRangesAndTheAskedPorts(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	cands := pickCandidates(rng, []int{2408, 500}, 6, 480, true)
	if len(cands) == 0 || len(cands) > 480 {
		t.Fatalf("%d candidates", len(cands))
	}
	seen := map[string]bool{}
	for _, c := range cands {
		if !inWarpRange(c.Addr.Addr()) {
			t.Fatalf("%s is outside the WARP ranges", c)
		}
		if p := c.Addr.Port(); p != 2408 && p != 500 {
			t.Fatalf("%s: port not asked for", c)
		}
		if seen[c.String()] {
			t.Fatalf("%s twice", c)
		}
		seen[c.String()] = true
		if last := c.Addr.Addr().AsSlice(); c.Addr.Addr().Is4() && (last[3] == 0 || last[3] == 255) {
			t.Fatalf("%s is a network or broadcast address", c)
		}
	}
	limited := pickCandidates(rng, []int{2408}, 24, 10, false)
	if len(limited) != 10 {
		t.Errorf("the limit gave %d", len(limited))
	}
}

func TestScanRequestIsBoundedAndNormalised(t *testing.T) {
	priv, pub := newKey(t)
	good := warpAccount{PrivateKey: b64(priv), PeerPublicKey: b64(pub), Address: []string{"172.16.0.2/32"}}
	r := scanRequest{Account: good}
	if err := r.normalize(); err != nil {
		t.Fatal(err)
	}
	if len(r.Ports) == 0 || r.PerRange != 8 || r.Top != 10 || r.SpeedTop != 3 {
		t.Errorf("defaults: %+v", r)
	}
	big := scanRequest{Account: good, PerRange: 9999, Top: 9999, SpeedTop: 99}
	if err := big.normalize(); err != nil || big.PerRange != 24 || big.Top != 30 || big.SpeedTop != 5 {
		t.Errorf("limits: %+v %v", big, err)
	}
	for name, bad := range map[string]scanRequest{
		"a foreign port":    {Account: good, Ports: []int{22}},
		"too many ports":    {Account: good, Ports: []int{500, 854, 859, 864, 878, 880, 890, 891, 894, 903, 908, 928, 934}},
		"no key":            {Account: warpAccount{Address: good.Address}},
		"no address":        {Account: warpAccount{PrivateKey: good.PrivateKey}},
		"junk out of range": {Account: good, AWG: awgParams{Jc: 99, Jmin: 40, Jmax: 70}},
		"jmax below jmin":   {Account: good, AWG: awgParams{Jc: 4, Jmin: 70, Jmax: 40}},
		"huge junk":         {Account: good, AWG: awgParams{Jc: 4, Jmin: 40, Jmax: 5000}},
	} {
		if bad.normalize() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// recorder is a progress sink that many probe workers may write at once.
type recorder struct {
	mu    sync.Mutex
	stage string
	done  int
}

func (r *recorder) set(stage string, done, _ int) {
	r.mu.Lock()
	r.stage, r.done = stage, done
	r.mu.Unlock()
}

func TestScanFindsNothingWhenNothingAnswers(t *testing.T) {
	// The real WARP ranges are not reachable from a test, so every probe times out; the scan has to end, say so and
	// not report phantom endpoints. A short context keeps the test quick.
	priv, pub := newKey(t)
	req := scanRequest{Account: warpAccount{PrivateKey: b64(priv), PeerPublicKey: b64(pub), Address: []string{"172.16.0.2/32"}}, PerRange: 1, Ports: []int{2408}}
	if err := req.normalize(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	res, err := runScan(ctx, req, &recorder{})
	if err == nil && len(res.Hits) != 0 {
		t.Errorf("hits without an answer: %+v", res.Hits)
	}
}

func TestTraceIsParsed(t *testing.T) {
	got := parseTrace(strings.NewReader("fl=1f1\nh=www.cloudflare.com\nip=104.28.1.2\ncolo=FRA\nloc=DE\nwarp=on\ngateway=off\n"))
	if got["warp"] != "on" || got["colo"] != "FRA" || got["ip"] != "104.28.1.2" || got["loc"] != "DE" {
		t.Errorf("trace = %v", got)
	}
}
