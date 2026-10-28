// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

// The WARP endpoint scanner. Cloudflare answers a device on many addresses and ports and not all of them are equally
// near or equally fast from where the panel runs. A scan draws addresses from the WARP ranges, asks each with a real
// handshake, keeps the quickest, asks those again for a steadier figure and, when told to, downloads through the best
// few to rank them by speed.

package main

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	probeTimeout  = 1500 * time.Millisecond
	probeWorkers  = 48
	maxCandidates = 480
	maxJunk       = 16

	// handshakeSpacing keeps repeated probes of one endpoint apart: WireGuard answers at most one initiation per 20 ms.
	handshakeSpacing = 40 * time.Millisecond
)

// scanRequest is what a scan is asked to do.
type scanRequest struct {
	Account  warpAccount `json:"account"`
	AWG      awgParams   `json:"awg"`
	MTU      int         `json:"mtu,omitempty"`
	Ports    []int       `json:"ports,omitempty"`    // default: defaultScanPorts
	PerRange int         `json:"perRange,omitempty"` // addresses drawn from each range (default 8)
	IPv6     bool        `json:"ipv6,omitempty"`
	Top      int         `json:"top,omitempty"`      // results to keep (default 10)
	Speed    bool        `json:"speed,omitempty"`    // rank the best few by a download
	SpeedTop int         `json:"speedTop,omitempty"` // how many of them (default 3, at most 5)
}

// scanHit is one endpoint the scan found alive.
type scanHit struct {
	Endpoint    string  `json:"endpoint"`
	RttMs       float64 `json:"rttMs"`
	LossPct     int     `json:"lossPct"`
	HandshakeMs int     `json:"handshakeMs,omitempty"`
	DownMbps    float64 `json:"downMbps,omitempty"`
	Tested      bool    `json:"tested,omitempty"` // the speed test ran for it
	Colo        string  `json:"colo,omitempty"`
}

type scanResult struct {
	Tried int       `json:"tried"`
	Alive int       `json:"alive"`
	Hits  []scanHit `json:"hits"`
}

func (r *scanRequest) normalize() error {
	if err := r.Account.validate(); err != nil {
		return err
	}
	if err := r.AWG.validate(); err != nil {
		return err
	}
	if len(r.Ports) == 0 {
		r.Ports = defaultScanPorts
	}
	if len(r.Ports) > 12 {
		return errors.New("at most 12 ports per scan")
	}
	for _, p := range r.Ports {
		if !isWarpPort(p) {
			return errors.New("a port that WARP endpoints do not use")
		}
	}
	r.PerRange = clamp(r.PerRange, 8, 1, 24)
	r.Top = clamp(r.Top, 10, 1, 30)
	r.SpeedTop = clamp(r.SpeedTop, 3, 1, 5)
	return nil
}

func (a awgParams) validate() error {
	if a.Jc < 0 || a.Jc > maxJunk || a.Jmin < 0 || a.Jmax < a.Jmin || a.Jmax > 1280 {
		return errors.New("junk packets: Jc 0-16, 0 <= Jmin <= Jmax <= 1280")
	}
	return nil
}

func clamp(v, def, lo, hi int) int {
	if v == 0 {
		v = def
	}
	return max(lo, min(hi, v))
}

// probe sends the junk packets and one handshake initiation to addr and waits for the answer. It returns the time from
// the initiation to the answer.
func probe(ctx context.Context, priv, peer [32]byte, awg awgParams, addr netip.AddrPort) (time.Duration, error) {
	c, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(addr))
	if err != nil {
		return 0, err
	}
	defer c.Close()
	for i := 0; i < awg.Jc; i++ { // junk first, as an AmneziaWG client does; a plain WireGuard server drops it
		size := awg.Jmin
		if awg.Jmax > awg.Jmin {
			size += rand.IntN(awg.Jmax - awg.Jmin + 1)
		}
		junk := make([]byte, max(size, 1))
		for j := range junk {
			junk[j] = byte(rand.UintN(256))
		}
		junk[0] = 0xff // never a WireGuard message type
		if _, err := c.Write(junk); err != nil {
			return 0, err
		}
	}
	sender := rand.Uint32()
	msg, err := buildInitiation(priv, peer, sender, time.Now())
	if err != nil {
		return 0, err
	}
	deadline := time.Now().Add(probeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.SetReadDeadline(deadline)
	start := time.Now()
	if _, err := c.Write(msg); err != nil {
		return 0, err
	}
	buf := make([]byte, 256)
	for {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		n, err := c.Read(buf)
		if err != nil {
			return 0, err
		}
		if isAnswer(buf[:n], sender) == nil {
			return time.Since(start), nil
		}
	}
}

// median of a non-empty list of durations.
func median(d []time.Duration) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

type progress interface {
	set(stage string, done, total int)
}

// runScan is the whole scan. The stages, and how far each got, are reported through prog.
func runScan(ctx context.Context, req scanRequest, prog progress) (scanResult, error) {
	priv, err := keyBytes(req.Account.PrivateKey)
	if err != nil {
		return scanResult{}, err
	}
	peer, err := keyBytes(req.Account.peerKey())
	if err != nil {
		return scanResult{}, err
	}
	cands := pickCandidates(rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())), req.Ports, req.PerRange, maxCandidates, req.IPv6)

	// Stage 1: one handshake to each candidate.
	type found struct {
		addr netip.AddrPort
		rtt  time.Duration
	}
	var (
		mu    sync.Mutex
		alive []found
		done  atomic.Int64
	)
	prog.set("probe", 0, len(cands))
	work := make(chan candidate)
	var wg sync.WaitGroup
	for i := 0; i < probeWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range work {
				if rtt, err := probe(ctx, priv, peer, req.AWG, c.Addr); err == nil {
					mu.Lock()
					alive = append(alive, found{c.Addr, rtt})
					mu.Unlock()
				}
				prog.set("probe", int(done.Add(1)), len(cands))
			}
		}()
	}
feed:
	for _, c := range cands {
		select {
		case work <- c:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return scanResult{}, err
	}
	res := scanResult{Tried: len(cands), Alive: len(alive)}
	if len(alive) == 0 {
		return res, nil
	}
	sort.Slice(alive, func(i, j int) bool { return alive[i].rtt < alive[j].rtt })

	// Stage 2: the quickest dozen, asked five times each, for a steadier figure and a loss rate.
	if len(alive) > 12 {
		alive = alive[:12]
	}
	const rounds = 5
	hits := make([]scanHit, 0, len(alive))
	for i, f := range alive {
		prog.set("measure", i, len(alive))
		var rtts []time.Duration
		for r := 0; r < rounds && ctx.Err() == nil; r++ {
			if r > 0 {
				time.Sleep(handshakeSpacing) // a WireGuard peer drops initiations that follow each other within 20 ms
			}
			if rtt, err := probe(ctx, priv, peer, req.AWG, f.addr); err == nil {
				rtts = append(rtts, rtt)
			}
		}
		if len(rtts) == 0 {
			continue
		}
		hits = append(hits, scanHit{
			Endpoint: f.addr.String(),
			RttMs:    float64(median(rtts).Microseconds()) / 1000,
			LossPct:  (rounds - len(rtts)) * 100 / rounds,
		})
	}
	if err := ctx.Err(); err != nil {
		return scanResult{}, err
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].LossPct != hits[j].LossPct {
			return hits[i].LossPct < hits[j].LossPct
		}
		return hits[i].RttMs < hits[j].RttMs
	})

	// Stage 3: a real download through the best few.
	if req.Speed {
		n := min(req.SpeedTop, len(hits))
		for i := 0; i < n && ctx.Err() == nil; i++ {
			prog.set("speed", i, n)
			p := tunnelParams{Account: req.Account, Endpoint: hits[i].Endpoint, MTU: req.MTU, AWG: req.AWG}
			cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
			chk := runCheck(cctx, p, true)
			cancel()
			hits[i].Tested = true
			if chk.OK {
				hits[i].HandshakeMs, hits[i].DownMbps, hits[i].Colo = chk.HandshakeMs, chk.DownMbps, chk.Colo
			}
		}
		if err := ctx.Err(); err != nil {
			return scanResult{}, err
		}
		sort.SliceStable(hits, func(i, j int) bool {
			if hits[i].Tested != hits[j].Tested {
				return hits[i].Tested
			}
			if hits[i].Tested {
				return hits[i].DownMbps > hits[j].DownMbps
			}
			return false
		})
	}
	if len(hits) > req.Top {
		hits = hits[:req.Top]
	}
	res.Hits = hits
	prog.set("done", 1, 1)
	return res, nil
}
