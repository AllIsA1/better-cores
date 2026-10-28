# better-cores

A plugin for the [LucX-UI](https://github.com/AlexeyLCP/lucx-ui) plugin system: it keeps the panel's tunnel cores up to date from the panel's own Cores page, and it is the
engine that **Better WARP** builds on.

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

## Core updates (0.4.0)

The plugin keeps the tunnel cores of the panel up to date, **inside the panel's own Settings → Cores page** (the page has the plugin's card and, in the
card of each core, its line; the plugin has no page and no sidebar entry of its own).

- **A look at the releases once a day**, and when you press *Check now*. It reads the last release of each project from GitHub; the time of the last look and
  of the next one are on the page. The look is kept across restarts, so a restart does not make it start over; a look that reached nobody is tried again in an hour.
- **Nothing is installed by itself.** A core with a release you have not installed shows *Install* / *Update to …*; the panel's copy stays until you press it.
- **An installed release replaces the panel's copy.** The plugin asks the panel to download the release file; the panel only accepts it if its SHA-256 is
  the one the plugin names (taken from the project's checksum file and from the digest GitHub computed for the file; if both exist they must agree, and a
  release with neither is refused), unpacks the archive, checks it is an ELF program, puts it in place of its own copy and restarts the core's running instances.
- **A panel update brings its own copy of every core**, so after one the plugin puts **the same release back** (never a newer one) within ten minutes.

| Core | Project | What is installed |
|---|---|---|
| OpenFlux | p1neappleXpress/OpenFlux | `openflux-linux-<arch>` |
| mieru | enfein/mieru | `mita` from `mita_<version>_linux_<arch>.tar.gz` |
| TrustTunnel | TrustTunnel/TrustTunnel | `trusttunnel_endpoint` from `trusttunnel-<tag>-linux-<cpu>.tar.gz` |

The other cores are not here on purpose: NaiveProxy's Caddy, AnyTLS (which needs the panel's `-cert`/`-key` patch), qWDTT and the Telegram proxy are builds of the
panel's own, olcRTC's fork and CSQTT publish no Linux program, so a release of the project is not a drop-in for them. The mieru and TrustTunnel *clients* the
panel keeps for outbound use are not touched either (the panel has no download for them).

Better WARP declares `"requires": ["better-cores"]`: the plugin system installs it only when Better Cores is installed and turned on, and refuses to turn
Better Cores off or remove it while Better WARP needs it.

The page reads the plugin through the plugin system (`/panel/api/plugins/x/better-cores/api/cores`), so it needs the panel build that has the Cores card
(`…-plugins.N` from lucx.329-plugins.2 on); on an older panel the plugin works but shows nothing.

## Permissions

- `network`: the WARP engine's packets to Cloudflare, and GitHub for the releases of the cores.
- `api:cores:write` (plugin system **host-11**): the panel's download of OpenFlux, mieru and TrustTunnel (`POST /tunnel/<core>/download`), nothing else of the cores.
- `api:server:read`: the panel's version, to see that a panel update has happened.

## Build

`make build` writes `bin/<arch>/bettercores` (commit `bin/`: the panel downloads the repository as it is). `make test` runs the tests.
The build is reproducible (`-trimpath -buildvcs=false`, CGO off): with Go 1.26.8, `make build` leaves `bin/` unchanged, which is how a reviewer checks that the
committed binaries are this source.

MIT, see `LICENSE`.
