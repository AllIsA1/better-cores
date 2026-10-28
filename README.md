# better-cores

A plugin for the [LucX-UI](https://github.com/AlexeyLCP/lucx-ui) plugin system: the engine that **Better WARP** builds on.

## The WARP engine

Better Cores carries the engine Better WARP needs: an AmneziaWG client **inside the plugin** (the userspace `amneziawg-go` with its own network
stack; it touches no interface and no route of the machine). It offers two things to a plugin that requires it and holds `peer:better-cores`
(plugin system **host-9** and later; the call goes through the plugin system's gateway, `/peer/better-cores/warp/...`, and nothing else of this plugin
is reachable that way):

- **Check** (`POST /peer/warp/check`): brings a tunnel up to a WARP endpoint with the account's key (junk packets as an AmneziaWG client sends them),
  waits for the handshake, asks the exit for `cdn-cgi/trace` (`warp=on|plus`, the exit's address and location, the round trip) and, if asked,
  downloads for a few seconds through four streams to measure the speed.
- **Scan** (`POST /peer/warp/scan`): draws addresses from Cloudflare's WARP ranges, sends each one a real WireGuard handshake initiation made with the
  account's key (a WARP endpoint answers only a key it knows) and keeps the quickest; asks the quickest dozen five times for a steadier round trip and a
  loss rate; and, if asked, downloads through the best few to rank them by speed.

Both run as jobs (start with one call, read `GET /peer/warp/jobs/<id>`, stop with `DELETE`); two run at once at most. The engine sends packets only to
Cloudflare's WARP ranges (`162.159.192/193/195.0/24`, `188.114.96-99.0/24`, `2606:4700:d0/d1::/48`, the name `engage.cloudflareclient.com`) and to the
ports WARP endpoints use, so it cannot be turned into a general scanner. What it measures is what **this machine** sees: the quickest endpoint for the
server is not always the quickest for a client far from it.

## What it no longer does (0.3.0)

Up to 0.2.0 the plugin also listed the tunnel cores of the panel with the last upstream release of each. Nothing used that list (the plugin has no
page, and the panel's own Cores page shows the cores), and Better OpenFlux and Better olcRTC, which required this plugin, have nothing left to do since the panel
brings OpenFlux v0.13.0 and the olcRTC fork itself (lucx.325/326). So the list, the GitHub lookup and the permissions it needed are gone.

The plugin has **no page and no sidebar entry**. Better WARP declares `"requires": ["better-cores"]`: the plugin system installs it only when Better Cores is installed and turned
on, and refuses to turn Better Cores off or remove it while Better WARP needs it.

## Permissions

`network` (the WARP engine's packets to Cloudflare, nothing else).

## Build

`make build` writes `bin/<arch>/bettercores` (commit `bin/`: the panel downloads the repository as it is). `make test` runs the tests.
The build is reproducible (`-trimpath -buildvcs=false`, CGO off): with Go 1.26.8, `make build` leaves `bin/` unchanged, which is how a reviewer checks that the
committed binaries are this source.

MIT, see `LICENSE`.
