# akari-client

Desktop client for Akari panel users: a system-tray app that fetches the
user's Clash profile from the panel and runs the
[mihomo](https://github.com/MetaCubeX/mihomo) kernel (v1.19.32, GPL-3.0) as a
**separate process** to provide a local mixed proxy (HTTP + SOCKS5) on
`127.0.0.1:7890`, optionally set as the system proxy.

Status: **M4 MVP** (Windows → macOS → Linux). Installers, code signing and
the Windows manual test pass are still open — see docs/PACKAGING.md.
Non-goals: rule editing, subscription aggregation, TUN mode, mobile.

## Features

- Sign in once: subscription URL (`https://<host>/<prefix>/sub/<token>`) or
  panel URL + token, via the local settings page or `akari-client login`.
- Profile fetch with UA `akari-client/<ver> mihomo`, header
  `X-Akari-Device` (M5 seat binding placeholder), ETag/If-None-Match, usage
  and expiry from `subscription-userinfo`; periodic + on-demand refresh;
  invalid or rejected profiles never replace the running one.
- The panel profile is sanitized (only proxies/groups/rules are used) and
  validated by the kernel (`mihomo -t`) before use.
- Kernel lifecycle: start/stop/in-place reload, immediate restart on exit,
  health checks, exponential backoff; kernel dies with the client.
- System proxy on/off (Windows WinINet, macOS networksetup, Linux
  GNOME/KDE), reverted on disconnect/quit.
- Node menu with latency test; selection persisted.
- Self-heal after network change or resume from sleep.
- JSON logs with rotation; single instance.

## Layout

| Path | Purpose |
|---|---|
| `cmd/akari-client` | entry point: tray (default), `--headless`, `login`, `version` |
| `internal/core` | **the only mihomo-aware package**: config builder, process engine, REST client |
| `internal/app` | controller: login/refresh/connect/system proxy/node selection/self-heal |
| `internal/supervisor` | desired-state loop, health checks, backoff |
| `internal/subscription` | URL normalization, fetch (ETag, fallback via kernel), refresh scheduler |
| `internal/sysproxy` | per-OS system proxy plans (pure) + executors |
| `internal/netwatch` | network change / resume detection |
| `internal/settings`, `device`, `instance`, `logging`, `paths`, `store` | persistence & plumbing |
| `internal/ui/tray`, `internal/ui/web` | tray menu, local settings page |
| `third_party/mihomo` | separate Go module pinning the kernel build (GPL-3.0) |

On-disk (per user, 0700/0600): `%AppData%\Akari` (Windows),
`~/Library/Application Support/Akari` (macOS), `~/.config/Akari` (Linux):
`settings.json`, `profile.yaml`, `device.json`, `logs/`, `core/` (kernel
config with node credentials and controller secret).

## Build & test

Go 1.27 (`go.mod`). Linux/Windows builds are pure Go; the macOS client needs
cgo and a macOS host.

    make build        # bin/akari-client + bin/mihomo for the host
    make test         # builds the kernel, go test -race ./... (incl. real-kernel tests)
    make ci           # fmt-check, vet (linux/windows/darwin), check-boundary, test, build, govulncheck (client + kernel)
    make dist         # dist/{windows,linux}-amd64/ + licenses + SHA256SUMS
    make dist-darwin  # on macOS only

Run locally without the tray:

    ./bin/akari-client --data-dir /tmp/akari login https://panel.example.com/<prefix>/sub/<token>
    ./bin/akari-client --data-dir /tmp/akari --headless

## Licensing

akari-client is proprietary (LICENSE). mihomo is GPL-3.0 and is shipped as an
unmodified separate program; it must never be linked into the client
(`make check-boundary`). See THIRD-PARTY-NOTICES.md and docs/DECISIONS.md D1.
