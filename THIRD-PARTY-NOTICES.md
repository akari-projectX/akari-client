# Third-party notices (akari-client)

akari-client is licensed under the GNU GPL v3.0 (see LICENSE). A distribution consists of
two programs:

1. **akari-client** (GPL-3.0) — statically links only the permissively
   licensed Go modules below.
2. **mihomo** (GPL-3.0) — a separate, unmodified executable shipped next to
   akari-client and started by it as a child process. It is kept out of the
   client binary for robustness (crash isolation, reload races — see
   docs/DECISIONS.md D1), enforced by `make check-boundary`.

`make dist` writes, per platform, `THIRD-PARTY-LICENSES.txt` (full license and
NOTICE texts of every module linked into akari-client, generated from the
module cache by `scripts/licenses.sh`) and `mihomo-LICENSE.txt` (GPL-3.0).
Installers must ship both files.

## Linked into akari-client

| Component | Version | License | Notes |
|---|---|---|---|
| [fyne.io/systray](https://github.com/fyne-io/systray) | v1.12.2 | Apache-2.0 | system tray; NOTICE below |
| [godbus/dbus](https://github.com/godbus/dbus) | v5.1.0 | BSD-2-Clause | Linux tray (StatusNotifierItem) |
| [natefinch/lumberjack](https://github.com/natefinch/lumberjack) | v2.2.1 | MIT | log rotation |
| [go-yaml/yaml](https://github.com/go-yaml/yaml) | v3.0.1 | MIT and Apache-2.0 | subscription parsing |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | v0.48.0 | BSD-3-Clause | registry, job objects, file locks |
| Go standard library | 1.27 | BSD-3-Clause | |

fyne.io/systray NOTICE:

```
Copyright 2011-2016 Canonical Ltd.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
```

## Shipped alongside: mihomo (GPL-3.0)

- Project: [MetaCubeX/mihomo](https://github.com/MetaCubeX/mihomo), tag
  **v1.19.32**, unmodified.
- License: GNU General Public License v3.0 (`mihomo-LICENSE.txt` in every
  distribution).
- Corresponding source: the upstream tag plus this repository's
  `third_party/mihomo/` directory (Go module pins and build command; provided
  under GPL-3.0 as part of that source). Release process (docs/PACKAGING.md):
  publish a source archive of exactly that for each release and reference it
  from the installer / About text, or include a written offer valid for three
  years (GPL-3.0 §6).
- mihomo is a derivative of Clash (Dreamacro, GPL-3.0); copyright notices are
  in the upstream source.

Note: earlier planning documents (`akari-panel/PLAN.md`, this repo's
`LICENSE` and earlier `README.md`/`CLAUDE.md`) described mihomo as MIT. That
is incorrect for v1.19.32; see docs/DECISIONS.md D1.
