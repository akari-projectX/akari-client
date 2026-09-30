# akari-client

自研代理客户端，内嵌 mihomo（v1.19.31，MIT）。**尚未开工**，仓库只有 README 与 LICENSE（专有）。

开工前必读：`../akari-panel/PLAN.md` 的 Phase 2（MVP）与 Phase 3（席位绑定）。

- 第一步是 1 周 spike：库内嵌 mihomo（推荐）还是子进程 + external controller。结论写回 PLAN.md 决策表。
- 必须 pin mihomo 版本，并把对 mihomo 的调用收敛到单独的包里（它的库 API 不承诺稳定）。
- 面板契约：MVP 阶段消费 `/{prefix}/sub/{token}` 的 Clash 格式（UA 需包含 `mihomo` 或 `clash`）；Phase 3 迁到 `/client/v1/*` REST。
- 许可：分发时保留 mihomo 的 MIT 版权声明（仿照 agent 仓的 THIRD-PARTY-NOTICES.md）。
- 明确不做：规则编辑、订阅聚合、TUN、移动端（二期）。
