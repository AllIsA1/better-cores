# better-cores

A plugin for the [LucX-UI](https://github.com/AlexeyLCP/lucx-ui) plugin system: the tunnel **cores** of the panel at a glance.

## What it does (0.1.x)

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
OpenFlux), `network` (GitHub's releases API, nothing else).

## Build

`make build` writes `bin/<arch>/bettercores` (commit `bin/`: the panel downloads the repository as it is). `make test` runs the tests.
The build is reproducible (`-trimpath -buildvcs=false`, CGO off): with Go 1.26.8, `make build` leaves `bin/` unchanged, which is how a reviewer checks that the
committed binaries are this source.

MIT, see `LICENSE`.
