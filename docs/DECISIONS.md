# akari-client decisions

Status as of 2026-10-02 (M4 MVP, branch `feat/m4-mvp`). Each entry: decision,
why, consequences. Supersedes the "client kernel" row of
`../akari-panel/PLAN.md` §三 where they conflict (see D1).

## D1 — Kernel integration: separate mihomo process + REST controller (not library embedding)

**Decision.** akari-client runs mihomo **v1.19.31** as a separate, unmodified
executable shipped next to it (`mihomo` / `mihomo.exe`) and controls it only
through its CLI (`-d -f -t -v`) and its external-controller REST API on
`127.0.0.1:<random port>` with a random 256-bit per-run secret. All of that
lives in `internal/core`; no other package knows mihomo's config schema, CLI
or API.

**Spike result (library embedding).** Commit `886dfdd` embedded mihomo
in-process (`config.ParseRawConfig` + `hub/executor.ApplyConfig`, stop via an
empty config, `tunnel.Proxies()` for selection and URL tests). It worked: the
integration test proxied HTTP through the embedded kernel in 0.1 s. It was
rejected for two reasons:

1. **License — blocking.** mihomo is **GPL-3.0**, not MIT (verified in the
   v1.19.31 module source: `LICENSE` is the GNU GPL v3; upstream Clash was
   GPL-3.0 too). PLAN.md's premise "MIT allows closed-source embedding" is
   wrong. Linking it into akari-client would make the client a combined work
   that must be distributed under the GPL. Running it as a separate program
   that the client talks to over sockets/command line is the standard
   "aggregate" arrangement (FSF GPL FAQ, *MereAggregation*), the same model
   used by the many third-party Clash GUIs. **Legal review is recommended
   before the first public release.**
2. **Engineering.** mihomo keeps its runtime in package globals; the race
   detector found unsynchronized globals written by `ApplyConfig` and read by
   live listener goroutines (`adapter/inbound` ipfilter) on every reload, and
   any panic in a mihomo goroutine would have killed the tray UI. A child
   process gives crash isolation and an exact failure signal (process exit).

**Consequences.**
- `make check-boundary` (CI) fails if the client module requires or links any
  `github.com/metacubex/*` package. Only `third_party/mihomo` — a separate Go
  module containing nothing but dependency pins — builds the kernel.
- GPL obligations for the kernel binary: ship its license text, offer the
  corresponding source (upstream tag v1.19.31 + `third_party/mihomo/` build
  files), do not modify it. See THIRD-PARTY-NOTICES.md and docs/PACKAGING.md.
- Two executables per platform (client ≈ 10 MB, kernel ≈ 50 MB stripped).
- The kernel is tied to the client's lifetime: Windows job object
  (`KILL_ON_JOB_CLOSE`), Linux `Pdeathsig=SIGKILL`, and on every platform a
  pid file in the private core dir; a leftover kernel of a crashed client is
  killed on the next start (only if its image path is our binary).
- The kernel's environment is scrubbed of `CLASH_*`/`MIHOMO_*`/`SAFE_PATHS`
  and proxy variables.
- Integration surface to re-check on a version bump (all in `internal/core`):
  config keys written by `BuildConfig`; CLI `-d -f -t -v`; REST `GET /version`,
  `GET|PUT /configs?force=true`, `GET /proxies`, `PUT /proxies/{group}`,
  `GET /proxies/{name}/delay`, `GET /group/{name}/delay`,
  `DELETE /connections`, `POST /cache/dns/flush`. Bump procedure: change
  `third_party/mihomo/go.mod`, `MIHOMO_VERSION` (Makefile) and
  `core.MihomoVersion`, run `make ci` (the integration tests run the real
  kernel and check the pinned version).
- Upstream quirk: the delay API reports a 0 ms result as a failure (only
  matters on loopback; tests add 3 ms of latency).

## D2 — The subscription is untrusted input: whitelist, then kernel validation

Only `proxies`, `proxy-groups` and `rules` are taken from the panel's Clash
profile. Everything else — listeners, ports, `allow-lan`, external
controller/UI, secret, TUN, DNS, authentication, providers, group `use:` —
is dropped and replaced by fixed values (mixed port on 127.0.0.1 only, rule
mode, no providers, no geo auto-update). A compromised or misconfigured panel
therefore cannot open a LAN listener or a control API on the user's machine
or make the client fetch anything but the subscription. Structural checks
(unique names, known group members) run in Go; full validation runs the
kernel's own `mihomo -t` before a new profile is adopted. A profile that fails
either check is never persisted; the running profile stays.

## D3 — Tray: fyne.io/systray v1.12.2

Apache-2.0 (NOTICE reproduced in THIRD-PARTY-NOTICES.md), maintained fork of
getlantern/systray (also Apache-2.0, unmaintained). Windows: pure Go. Linux:
pure Go via D-Bus StatusNotifierItem (godbus, BSD-2-Clause) — no GTK/cgo,
needs a desktop with an SNI host (KDE, GNOME + AppIndicator extension, ...).
macOS: cgo/Cocoa, so darwin builds run on a macOS host (CI: macos-latest).
Node entries are pre-allocated (128) and hidden when unused because menus
cannot be rebuilt reliably on every platform.

## D4 — Device identity (M5 placeholder)

`device.json` holds a random UUIDv4 generated on first run. It is sent as
`X-Akari-Device` on every subscription fetch (the panel ignores it until M5).
A salted SHA-256 of the OS machine id (`/etc/machine-id`, Windows
`MachineGuid`, macOS `IOPlatformUUID`) is stored next to it and **never
sent**: it only detects a profile directory copied to another machine
(cloned VM, synced AppData), in which case a new UUID is generated. Raw
machine ids are not written anywhere. PLAN Phase 3 says "device_id =
hardware fingerprint hash"; a random id is used instead because it is
privacy-safe (not linkable across apps/reinstalls) and still stable; M5 can
add a server-issued device credential on top.

## D5 — Sign-in UX: local settings page + CLI

The tray cannot take text input, so "Settings…" opens a small page served on
`127.0.0.1:<random>` under a random 256-bit path secret, with an exact `Host`
check (DNS rebinding), same-origin check on POST, strict CSP,
`Referrer-Policy: no-referrer` and `no-store`. It takes the subscription URL
(or panel URL `https://host/<prefix>` + token), mixed port and refresh
interval. Headless/CLI: `akari-client login <url> [token]`. Input is
normalized (`…/sub/<43-char token>`), https is required except for loopback.

The subscription URL (secret prefix + token = credential) is stored in
`settings.json` (0600, dir 0700) and never logged in full (`Redact` keeps only
scheme + host). TODO (M5): move it to the OS credential store
(DPAPI/Keychain/libsecret).

## D6 — Subscription fetch and refresh

- UA `akari-client/<version> mihomo` (panel serves Clash for "mihomo").
- Direct connection first (explicitly no proxy, so a stale system proxy
  pointing at a stopped kernel cannot break it); on a network-level failure
  the fetch is retried through the running kernel (panel only reachable via
  the proxy). HTTP-level answers are never retried through the kernel.
- `If-None-Match` when the panel sent an `ETag` (it currently does not);
  304 keeps the profile and updates usage info.
- 404 is the panel's single rejection (unknown/rotated token, disabled or
  expired account, rate limit): surfaced as such; the old profile keeps
  running.
- Interval: user setting > panel `profile-update-interval` (hours) > 12 h;
  floor 15 min. Failures retry 1 m, 2 m, 4 m … 30 m (never later than the
  interval). Body cap 8 MiB.

## D7 — Lifecycle, crash restart, self-heal

`internal/supervisor` owns the desired state. Start failures (port in use,
kernel rejects the profile) and crashes back off 1 s → 60 s (reset after
2 min healthy). A kernel exit is noticed immediately (process wait) — E2E:
`kill -9` of the kernel → serving again in ≈1 s. Health checks every 10 s
(API `/version` + SOCKS5 greeting on the mixed port); two consecutive
failures restart the kernel.

Network change / resume (`internal/netwatch`: 5 s poll of interface
addresses; wall-vs-monotonic clock gap for sleep): close all connections,
flush kernel DNS, probe the selected node (3 tries, 2/4/6 s apart), reload the
kernel if it stays unreachable.

The process-level watchdog of the spike was removed: with the kernel out of
process, a kernel crash no longer takes the UI down.

## D8 — System proxy

Applied when the kernel is running and the preference is on; reverted on
Disconnect and on Quit (the preference persists and is re-applied on the next
launch). Disable only touches settings that still point at our endpoint, so a
proxy the user configured meanwhile is left alone; on startup with the
preference off, a stale setting pointing at us (crash) is cleaned up.
- Windows: WinINet registry values under HKCU (`ProxyEnable`, `ProxyServer`,
  `ProxyOverride` with wildcard bypasses + `<local>`) followed by
  `InternetSetOption(SETTINGS_CHANGED, REFRESH)`. Limitation: per-connection
  (RAS/VPN) entries and WinHTTP-only apps are not covered.
- macOS: `networksetup` web/secure-web/SOCKS proxy + bypass domains on every
  enabled network service.
- Linux: GNOME `gsettings` and/or KDE `kwriteconfig5/6` + KIO reparse signal,
  chosen from `$XDG_CURRENT_DESKTOP` and tools on PATH; other desktops: clear
  error, configure manually.

## D9 — Logs, single instance

JSON logs (`log/slog`) in `<config dir>/Akari/logs/akari-client.log`, rotated
at 10 MB, 5 compressed backups, 14 days (lumberjack). The kernel's output is
forwarded into the same log; its level is `warning` by default because
`info` logs every destination host — `-log-level debug` enables it.
Single instance: exclusive non-blocking lock on `akari-client.lock`
(flock / LockFileEx), released by the OS if the process dies.

## 2026-10-02: kernel bumped to v1.19.32
License unchanged (GPL-3.0); upstream replace directives unchanged; `make ci` green (real-kernel integration tests, govulncheck: only allow-listed GO-2026-5932).

## 2026-10-02: license — GPL-3.0
User decision: akari-client is open source under GPL-3.0 (LICENSE replaced). The separate-process kernel architecture is kept for its robustness benefits (crash isolation, mihomo reload data races), not for licensing; the library engine from 886dfdd remains an option but is not planned.
