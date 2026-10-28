// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

// Where the WARP engine may send packets. The engine is a peer call for other plugins, so it is held to Cloudflare's WARP
// endpoints and ports: it cannot be made a general UDP sender or scanner.

package main

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// warpHost is the name WARP clients resolve to reach an endpoint.
const warpHost = "engage.cloudflareclient.com"

// warpV4 and warpV6 are the address ranges Cloudflare's WARP endpoints live in.
var warpV4 = []netip.Prefix{
	netip.MustParsePrefix("162.159.192.0/24"),
	netip.MustParsePrefix("162.159.193.0/24"),
	netip.MustParsePrefix("162.159.195.0/24"),
	netip.MustParsePrefix("188.114.96.0/24"),
	netip.MustParsePrefix("188.114.97.0/24"),
	netip.MustParsePrefix("188.114.98.0/24"),
	netip.MustParsePrefix("188.114.99.0/24"),
}

var warpV6 = []netip.Prefix{
	netip.MustParsePrefix("2606:4700:d0::/48"),
	netip.MustParsePrefix("2606:4700:d1::/48"),
}

// warpPorts are the UDP ports a WARP endpoint answers on.
var warpPorts = []int{
	500, 854, 859, 864, 878, 880, 890, 891, 894, 903, 908, 928, 934, 939, 942, 943, 945, 946, 955, 968, 987, 988,
	1002, 1010, 1014, 1018, 1070, 1074, 1180, 1387, 1701, 1843, 2371, 2408, 2506, 3138, 3476, 3581, 3854, 4177,
	4198, 4233, 4500, 5279, 5956, 7103, 7152, 7156, 7281, 7559, 8319, 8742, 8854, 8886,
}

// defaultScanPorts are tried when a scan names none: the ports that most often work.
var defaultScanPorts = []int{2408, 500, 1701, 4500}

func isWarpPort(p int) bool {
	for _, q := range warpPorts {
		if p == q {
			return true
		}
	}
	return false
}

func inWarpRange(ip netip.Addr) bool {
	ranges := warpV4
	if ip.Is6() && !ip.Is4In6() {
		ranges = warpV6
	}
	for _, p := range ranges {
		if p.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

// checkEndpoint accepts "engage.cloudflareclient.com:port" and "ip:port" with an address in a WARP range.
func checkEndpoint(endpoint string) error {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(endpoint))
	if err != nil {
		return errors.New("the endpoint must be host:port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || !isWarpPort(port) {
		return fmt.Errorf("port %q is not a port of the WARP endpoints", portText)
	}
	if strings.EqualFold(host, warpHost) {
		return nil
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !inWarpRange(ip) {
		return fmt.Errorf("%q is neither %s nor an address in Cloudflare's WARP ranges", host, warpHost)
	}
	return nil
}

// candidate is an endpoint a scan may try.
type candidate struct {
	Addr netip.AddrPort
}

func (c candidate) String() string { return c.Addr.String() }

// pickCandidates draws up to perRange random addresses from each range and pairs them with every port, at most limit
// in all.
func pickCandidates(rng *rand.Rand, ports []int, perRange, limit int, v6 bool) []candidate {
	ranges := append([]netip.Prefix(nil), warpV4...)
	if v6 {
		ranges = append(ranges, warpV6...)
	}
	var out []candidate
	for _, p := range ranges {
		seen := map[netip.Addr]bool{}
		for len(seen) < perRange {
			ip := randomIn(rng, p)
			if seen[ip] {
				continue
			}
			seen[ip] = true
			for _, port := range ports {
				out = append(out, candidate{netip.AddrPortFrom(ip, uint16(port))})
			}
		}
	}
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// randomIn is a random usable address of p (not the first, not the last, for the /24s).
func randomIn(rng *rand.Rand, p netip.Prefix) netip.Addr {
	a := p.Addr().AsSlice()
	bits := len(a)*8 - p.Bits()
	if bits > 16 {
		bits = 16 // the host part we vary: plenty of room, and an IPv6 /48 is not walked end to end
	}
	n := uint32(rng.IntN(1<<bits-2)) + 1
	for i := len(a) - 1; i >= 0 && n > 0; i-- {
		a[i] |= byte(n)
		n >>= 8
	}
	ip, _ := netip.AddrFromSlice(a)
	return ip
}
