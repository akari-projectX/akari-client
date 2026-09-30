# onyx-client

自研代理客户端（规划中，尚未开工）——内嵌 [mihomo](https://github.com/Metacubex/mihomo)
内核（v1.19.31，MIT），面向 Onyx 面板的最终用户。

定位与边界（详见 ../onyx-panel/PLAN.md）：

- **它是设备限制的信任锚**：客户端注册设备实现席位绑定，第三方客户端因此被
  排除在终态之外——这也是面板订阅接口（过渡期）最终只保留 Clash 格式的
  原因。
- MVP 功能集：设备注册 → 拉取订阅（Clash）→ 内核生命周期 → 本地 mixed
  端口 → 系统代理开关 → 基础托盘 UI。平台顺序：Windows → macOS → Linux。
- 明确不做（二期）：规则编辑、订阅聚合、TUN、移动端。
- 集成方式待 spike 定案：库内嵌（推荐）vs 子进程 + external controller。

开发启动前先读 `../onyx-panel/PLAN.md` 的 Phase 2/3 与风险清单
（mihomo 内嵌 API 非稳定契约：pin 版本、锁定集成面）。
