# akari-client

自研桌面客户端（M4 MVP，已暂停）。Go 1.27；托盘 UI + 本地设置页；
**mihomo v1.19.32 作为独立子进程**运行，经其 external-controller REST API 控制。

必读：`README.md`、`docs/DECISIONS.md`（D1–D9）、`docs/PACKAGING.md`（M4 剩余项）、
`THIRD-PARTY-NOTICES.md`。

## 硬规则

- **许可证：akari-client 为 GPL-3.0 开源**（2026-10-02 用户决定；mihomo 同为 GPL-3.0）。每次发布须公开客户端与内核的对应源码。
- 内核仍以独立子进程运行（崩溃隔离、避开 mihomo 重载时的全局变量竞态，见 D1），客户端模块不 import `github.com/metacubex/*`；内核只在 `third_party/mihomo`（独立 Go 模块）构建，原样随客户端分发。`make check-boundary` 在 CI 中把关。
- 所有 mihomo 知识（配置键、CLI `-d -f -t -v`、REST 端点）只在 `internal/core`。升级内核：
  改 `third_party/mihomo/go.mod` + Makefile `MIHOMO_VERSION` + `core.MihomoVersion`，跑 `make ci`。
- 订阅是不可信输入：只取 proxies / proxy-groups / rules（`core.BuildConfig` 白名单），
  再用 `mihomo -t` 校验；失败绝不替换正在运行的 profile。
- 订阅 URL（前缀 + token）是凭据：只存 `settings.json`（0600），日志只写 `subscription.Redact`。
- 内核日志默认 `warning`（`info` 会把访问的目标主机写盘）。
- 面板契约：UA `akari-client/<ver> mihomo` → Clash YAML；404 = 统一拒绝；附带 `X-Akari-Device`（面板忽略）。
  **URL 形状已过期**：代码与测试按旧的 `https://<host>/<prefix>/sub/<token>` 规整，而当前面板的订阅是
  `<订阅域名或主域名>/<订阅路径>/<token>`（无 `/sub/` 段、不含后台前缀）；恢复开发时先改 `internal/subscription` 与设置页。
  硬限制（客户端内登录 + 席位）尚未设计，见面板 `docs/DEPLOY.md`「Akari 客户端对接（规划）」。

## 验收门

`make ci`（fmt-check、vet linux/windows/darwin、check-boundary、`go test -race ./...` 含真实内核
集成测试、build、govulncheck 客户端 + 内核二进制）。darwin 完整构建只能在 macOS（CI macos-latest）。

## 明确不做

规则编辑、订阅聚合、TUN、移动端（二期）。
