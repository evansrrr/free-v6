---
feature: chain-proxy
status: delivered
updated: 2026-10-20
branch: feat/chain-proxy
commits: 9650916..802b41d
---

# 链式代理（本机 → masque → 自有代理 → 目标）

## Report

**What was built** — 设置页新增「链式代理」子页：Clash/mihomo 格式 yaml 文件（HTML file input）或订阅链接（helper 服务端拉取，仅导入时快照 + 手动刷新按钮）导入自有代理，含 2MB 上限、开关（导入自动开启）、清除。启动免流时若启用且已导入：解析注入 `dialer-proxy → 🚀 节点选择`（统一覆盖自带值，保证链路必经接入点）、追加 `🔗 链式代理` 分组、`MATCH` 改道该分组；随后用真实 `mihomo -t` 预校验，失败在网络快照前 400 拦下，启用却缺内容同样明确报错（绝不静默降级裸 WARP 出口）。接入点页出现「接入点 / 链式代理」双分组页签，各自读写对应 mihomo 分组。解析引入本仓库首个 Go 依赖 gopkg.in/yaml.v3（THIRD-PARTY 双清单 + 许可证文本随安装包分发）。审查发现的 CRITICAL——订阅节点名未转义进 `innerHTML` 的 XSS（可窃取 helper token）——已用共享 `escapeHTML` 封堵节点网格/日志/白名单全部非可信汇入点并复审通过。

**Verification** — `go build`/`go vet`/`go test ./... -count=1`/`node --check`/`cargo check` 全绿。E2E-A：真实代码路径渲染 + 真实 `mihomo -t` → successful。E2E-B：渲染产物（仅实验室补丁：关 TUN/底层 DIRECT/移除 LAN 与规则集/DNS 关——masque 无法送达实验室内环回内层、github 拉取会绕链死循环）跑通数据面并三轮连过：链路 200 → 切死节点 204 且请求被阻断 → 切回 204 恢复，全程 controller secret 生效。E2E-C：真实 helper 进程矩阵（导入/坏内容/GET 只回名称/DELETE/无 token 401/恶意 Host 403/预检 204/响应无凭证字段）全 PASS。独立审查报 1 critical（XSS）→ 修复 → 聚焦复审 APPROVE。前端 Tauri 实机路径未跑（需提权桌面应用），静态走查 + 双端 E2E 覆盖。

**Journey log**:
- 动手前用真实 mihomo 验证 `dialer-proxy` 指向分组（-t + 双实例数据面），承重设计先证后写。
- 渲染缩进 bug（链式块顶格 vs `proxies:` 2 空格项）被真实 -t 当场抓出——子串断言全过但没回读解析；已补 `yaml.Unmarshal` 回读单测防复发。
- 实验室 E2E 两大假警报源：PowerShell→curl 传参引号被破坏（body 带空格即 000/Body invalid，改 `--data-binary @file` 根治）；规则集后台拉 github 超时会推迟 controller 就绪且其自身流量绕链形成死循环（实验室特有）。
- yaml.v3 对 emoji 用 `\U` 转义、键按字母序重排——断言要对语义不对字节（`节点选择` 而非完整 emoji 行）。
- 校园网墙 proxy.golang.org，`GOPROXY=https://goproxy.cn,direct` 才能取依赖。

## [S1] Problem

用户希望把自有代理（机场）套进免流链路：出口 IP 为自有代理，链路 `本机 → masque(接入点) → 自有代理 → 目标网站`。设置页需要「链式代理」入口导入 yaml 文件或订阅链接；接入点页显示「接入点」「链式代理」两个分组并可分别切换选中节点。

## [S2] Design

### 用户已确认的决策

1. 解析：引入 `gopkg.in/yaml.v3`（本仓库首个 Go 外部依赖；同步 go.sum、THIRD-PARTY.txt、LICENSES）。
2. 订阅：**仅导入时拉取**（快照落盘），另提供手动「刷新」按钮；启动免流不联网拉订阅。
3. 交互：设置子页含**开关**（`chainEnabled`，导入成功后自动置开）+ 导入/刷新/清除。

### 数据面（渲染，internal/mihomo）

- `RenderOptions` 增加 `ChainProxies []ChainProxy`（`{Name, YAML}`，YAML 为该条目重发射后的块）与隐含开关（列表非空即启用）。
- 启用时渲染差异（**未启用时输出与现状逐字节一致**）：
  1. 每个导入条目**统一覆盖注入** `dialer-proxy: "🚀 节点选择"`——连接自有代理先过所选接入点，保证免流链路契约（用户 yaml 自带的内层链被替换，写明于此）；
  2. 追加分组 `🔗 链式代理`（select，成员 = 导入节点名）；
  3. 末条规则由 `MATCH,🚀 节点选择` 改为 `MATCH,🔗 链式代理`。
- 校园网/黑名单/`GEOIP,LAN` DIRECT 规则仍在最前，白名单与黑名单行为不变。
- 名称校验：导入名与内置组/节点名冲突或导入内重复 → 渲染报错（400，明确指出冲突名）。
- 链式分组**不加 DIRECT 兜底**：自有代理全挂时表现为不可用而非悄悄改变出口语义。

### 导入与存储（helper）

- 原始内容：`config/chain-proxy.yaml`（0600，**2MB 上限**）；settings.json 新增字段 `chainEnabled: bool`（默认 false）、`chainSourceType: "file"|"url"`、`chainSourceUrl: string`。settings PUT 沿用「省略即保留」的既有契约（`jsonKeyPresent`）。
- 新端点（全部在 token + guard 之后）：
  - `GET /api/v1/chain-proxy` → `{enabled, sourceType, sourceUrl, count, names[]}`——**只回名称与计数，不回节点详情**（不泄露凭证字段）。
  - `PUT /api/v1/chain-proxy`，body 二选一：
    - `{content}`：前端 file input 读出的文本（≤2MB）；
    - `{url}`：helper **服务端拉取**（30s 超时、2MB 截断）——导入与手动刷新共用，刷新即再次 PUT `{url}`。
    成功：解析校验 → 写盘 → 记录 source → `chainEnabled=true` → 返回 `{count, names}`；失败返回 4xx/5xx 带明确原因（非 Clash 格式、无 proxies、超限、拉取失败…）。
  - `DELETE /api/v1/chain-proxy`：删文件、清 source、`chainEnabled=false`。
- 解析：yaml.v3 解出 `proxies` 列表；条目须为含 `name` 与 `type` 的映射，否则报错定位（第 N 项）；仅转换 proxies，订阅自带的 rules/groups 一律丢弃。

### 启动流程（helper startProxy）

- `chainEnabled && 文件存在` 时：读文件 → 解析注入 → 合并渲染；随后（仅链式启用时）执行 **`mihomo.exe -t` 预校验**（新 `mihomo.TestConfig`，5s 超时），失败 → 400 返回 mihomo 原文，**发生在快照/网络改动之前**（当前顺序：渲染→geodata→写配置→自愈→快照，插在写配置后）。
- 链式未启用：流程与现状完全一致（不跑 `-t`，零回归面）。实施时 `-t` 超时取 15s（spec 初稿 5s，为 geodata 较慢机器留余量）。
- 校验失败**不启动**并明确报错——不做静默降级（否则用户以为出口是自有代理、实际是 WARP）。

### 前端（web）

- **设置 → 核心组**新增可点项「链式代理」→ 子页（复用白名单 `cidr-page` 模式：返回按钮 + 独立页）：
  - 订阅 URL 输入 + 「导入」；「刷新」仅 `sourceType=url` 时显示；
  - 「选择文件」（隐藏 `<input type="file" accept=".yaml,.yml">`，FileReader 取文本，无需新 Tauri 插件）；
  - `md-switch` 开关（chainEnabled）、状态行（来源 / N 个节点）、「清除」（确认后调 DELETE）。
  - 导入/刷新成功后 `state.chain` 更新并 toast/日志。
- **接入点页**：`state.chain.count > 0` 时顶部渲染两个 `md-filter-chip`（接入点 / 链式代理）；网格数据源切换：接入点 = 现状（`♻️ 自动选择` 的 all/now），链式 = `🔗 链式代理` 的 all/now；点节点 PUT 对应分组（链式 PUT 组名 `🔗 链式代理`）。
- `refreshBackendState` 增加一次 `GET /chain-proxy`（与 `/runtime` 同模式，失败忽略）填充 `state.chain`。

### 风险与已知项

- **R1（实施第一步）**：手工 curl 切换链式分组的 PUT 曾回 `Body invalid`(400)，而现网接入点切换同 API 正常——先对齐现网请求的字节形态再复用，再继续 T4。
- Tauri/Rust **零改动**；`mihomo -t` 仅链式启用时执行；无导入用户全路径行为不变。
- 延迟测试对链式节点会走全链路（更慢），沿用现有 5s 超时，超时显示 5000ms。

## [S3] Out of Scope

- 按域名/规则选择性走链（v1 全量 MATCH 走链式，白名单除外）。
- `mode: global/direct` 下 MATCH 不参与路由，链式不生效（链式语义绑定规则模式）。
- `RULE-SET` 命中的国内流量仍路由到接入点组（WARP 出口），仅 MATCH 兜底流量走自有代理出口——如需国内流量也走自有代理需后续改规则目标，待产品确认。
- 启动时自动刷新订阅、链式节点健康度/延迟的后台探测。
- 订阅协议除 Clash/mihomo yaml 以外的格式（ssr/clash 明文转换等）。
- 出口 IP 可视化校验、双出口统计。
- Tauri 侧任何改动。

## Tasks

- [x] T1: 引入 yaml.v3；`ParseChainProxies`（解析/校验/注入 dialer-proxy/名称冲突检测）+ 渲染集成 + 单测 — acceptance: `go test ./internal/mihomo` 全绿；链式关闭时渲染输出与旧版逐字节一致；开启时含 dialer-proxy、链式分组、MATCH 改道；冲突名/坏条目报错 (covers: S2 数据面)
- [x] T2: helper 端点 GET/PUT/DELETE `/api/v1/chain-proxy` + settings 三字段（省略即保留）+ 单测 — acceptance: `go test ./cmd/freev6-helper` 全绿；文件/URL 两路径、2MB 上限、只回名称计数、DELETE 清干净 (covers: S2 存储; depends: T1)
- [x] T3: startProxy 链式合并渲染 + `mihomo.TestConfig`(-t) 预校验 + 单测/E2E — acceptance: 链式启用时坏配置在快照前被 400 拦下；未启用时零变化；E2E 渲染产物 `-t` 通过 (covers: S2 启动; depends: T1)
- [x] T4: 前端设置子页（导入/刷新/清除/开关）+ 接入点双分组 + `GET /chain-proxy` 状态接入 — acceptance: `node --check` 通过；走查覆盖导入/刷新/切换/清除路径；**先解决 R1**（对齐现网切换请求形态，链式 PUT 实测可用） (covers: S2 前端; depends: T2)
- [x] T5: 端到端验证 + 回归 — acceptance: 真实 mihomo 对合并渲染配置 `-t` 通过；双实例数据面用**本功能渲染产物**复跑 PASS；helper 端点矩阵（token/guard 下）全 PASS；`go test ./...`/`vet`/`node --check` 全绿；无链式回归冒烟与上轮一致 (covers: S2 全部; depends: T3, T4)

> 审查后补丁（计入交付范围）：CRITICAL XSS 修复——`escapeHTML` 覆盖节点网格/日志/白名单 innerHTML 汇入点（commit 802b41d，聚焦复审 APPROVE）；THIRD-PARTY 双清单重编号去重。
