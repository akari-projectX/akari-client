# third_party/mihomo

Build pins for the mihomo kernel that ships **next to** akari-client as a
separate executable. This directory is a separate Go module; nothing in it is
linked into akari-client, and akari-client's own module must not depend on
mihomo (`make check-boundary`).

- Upstream: https://github.com/MetaCubeX/mihomo, tag **v1.19.31**, unmodified.
- License: **GPL-3.0** (upstream `LICENSE`). The files in this directory
  (`go.mod`, `go.sum`, `tools.go`) are build instructions for that program
  and are provided under the same license as part of its corresponding
  source.
- Deviations from upstream's own `go.mod`: newer versions of some
  dependencies to pick up security fixes (see `go.mod`; checked by
  `make vulncheck-mihomo`). No mihomo source file is changed.

Build (from the repository root): `make mihomo` (host) or `make dist`.
Equivalent command:

    cd third_party/mihomo && CGO_ENABLED=0 go build -trimpath -buildvcs=false \
      -ldflags '-s -w -buildid= -X "github.com/metacubex/mihomo/constant.Version=v1.19.31"' \
      -o ../../bin/mihomo github.com/metacubex/mihomo
