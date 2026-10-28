# better-cores

A plugin for the [LucX-UI](https://github.com/AlexeyLCP/lucx-ui) plugin system: the tunnel **cores** of the panel at a glance.

## The WARP engine (0.2.0)

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

## Cores at a glance (0.1.x)

- One row per core of the panel (OpenFlux, CSQTT, mieru, TrustTunnel, NaiveProxy, AnyTLS, MTProxy, olcRTC, qWDTT, Telegram WEB proxy):
  is the program in the panel, is it running (the panel's own `/tunnel/<core>/status`).
- The last release of the project each core comes from (olcRTC: the [ghostlane-project fork](https://github.com/ghostlane-project/olcrtc) that Better olcRTC
  installs, not the original; GitHub's public releases API, looked up in the background at most once an
  hour, so the page never waits for GitHub).
- It has **no page and no sidebar entry** (0.1.1): the plan is to extend the panel's own Cores page with its versions and extra buttons, not to add a menu.
- Updating cores from here is **not built yet**. The panel takes a core only as a ready file with its sha256, while most projects
  publish archives or Android packages (or no release at all); the plan for each core is still open.

Other plugins build on this one: Better OpenFlux and Better olcRTC declare `"requires": ["better-cores"]`, so the plugin system installs
them only when Better Cores is installed and turned on, and refuses to turn Better Cores off or remove it while one of them needs it.

## Permissions

`api:cores:read` (status of the cores other than OpenFlux; read only, no download, upload or logs), `api:openflux:read` (the same for
OpenFlux), `network` (GitHub's releases API, and the WARP engine's packets to Cloudflare).

## Build

`make build` writes `bin/<arch>/bettercores` (commit `bin/`: the panel downloads the repository as it is). `make test` runs the tests.
The build is reproducible (`-trimpath -buildvcs=false`, CGO off): with Go 1.26.8, `make build` leaves `bin/` unchanged, which is how a reviewer checks that the
committed binaries are this source.

MIT, see `LICENSE`.
