# FreeV6 工程改进重规划（2026-11-28）

> 复核对象：会话「项目工程纪律对比分析」中提出的批次一设计（应用更新签名 + 生命周期统一），以及该会话列出的后续批次方向。
>
> 代码基线：`0bc4cc0`（工作区干净）。本次复核实际执行了 `go test ./...`（全部通过）与 `node --test tests/*.test.js`（32 项通过）；Rust 侧本次未重跑（CI 配置为 `cargo check --locked`）；Windows 真机行为为代码审读结论，未做实机验证。
>
> 本文是设计复核 + 路线图，不是可直接执行的逐任务 TDD 计划。按既定工作方式：每批开始实施前，会先列出逐文件改动并征求确认，再另出该批的实施计划；提交信息使用英文。

---

## 0. 结论

### 0.1 对「目前的改动计划」的判定

**方向正确，但不能按原样实施。** 原计划对风险的识别基本准确（更新信任链、进程收尾、配置原子性、仓库门禁、前端巨文件），但有 5 处设计级问题必须先修正：

| # | 原计划设计 | 问题 | 修正方向 |
|---|---|---|---|
| 1 | 桌面壳创建随机名 Job Object，helper 必须加入，加入失败即失败退出 | 旧壳 + 新 helper 的混合版本（现场真实出现过，见 [main.rs:150](../../src-tauri/src/main.rs#L150) 的混合构建注释）会因拿不到 Job 名而直接无法启动；等于把「更新失败可排障」变成「更新失败即砖」 | **Job 改由 helper 自己创建并持有**，只约束 helper 的核心子进程；helper 不依赖桌面壳即可正常工作，混合版本自然降级 |
| 2 | 验收标准：强杀外壳后「无残留路由或热点共享状态」 | `KILL_ON_JOB_CLOSE` 直接 `TerminateProcess` 保不住网络状态；而且强制杀进程恰恰发生在还原热点之前，物理上无法兑现该承诺 | 区分「优雅收尾」与「硬杀兜底」：硬杀只保证无提权进程残留，网络状态由下次启动自愈；验收标准按此改写 |
| 3 | 已识别 `MarkVerified` 竞态，但用 Job/生命周期统一来「顺带」解决 | 两者无关。竞态根因是 `MarkVerified()` 标记的是**读取瞬间的 active**，而不是本次真正启动的版本 | `MarkVerified(version)` 版本精确匹配；只有标记成功才 `Prune()` |
| 4 | 本批只签应用更新，`f6-core` 签名「不处理」 | 内核清单与内核包同样来自同一镜像族，安装的也是会被提权执行的二进制——攻击面与应用更新完全同级，推迟等于留下同一个洞 | 应用清单签名与本批完成；核心清单签名紧随其后，作为同批的跨仓库交付项 |
| 5 | 引入设置修订号，把「配置内容 + 启用状态」做成类 Flux6 的事务 | 本仓库 settings 只有 helper 一个写者（CLI 不写），没有 Flux6 那种跨进程并发写场景；修订号解决的是不存在的问题 | 原子写 + 配置互斥 + 失败补偿 + 调整 reset 顺序；**不引入修订号** |

### 0.2 兼容性结论

按修正后的方案：

- **旧客户端（≤0.5.4）** 继续只读 `version.json`，新增的 `.sig` 资产对它完全透明；首个带签名的新版本仍可由旧客户端正常升级。
- **旧壳 + 新 helper**：所有新能力（shutdown 接口、父进程监视、Job）都是 helper 内部或附加接口；旧壳继续用「杀进程」收尾，功能不降级。
- **新壳 + 旧 helper**：`/api/v1/shutdown` 返回 404 时回落到 `/proxy/stop` + 杀进程；更新安装门禁只在「helper 确认未退出」时中止，不会误伤旧 helper。
- **CLI**：命令行参数不变；Win32 命名互斥量**对象名保持不变**（故意不重命名，保证新旧二进制仍能互斥）。
- **配置文件**：settings 只新增字段、不改变既有字段语义；链式/备用配置由「原地截断写」改为「临时文件 + rename」，读者只会看到旧版或新版。
- **有意的行为变化只有一处**：helper 被终止时，其核心子进程随 Job 一并退出（今天会留下无主核心）。这是需要确认的产品决策，理由见 §6。

### 0.3 性能结论

正常路径没有新增可感知等待，且有一处明显改善：

| 场景 | 现状 | 改后 | 说明 |
|---|---|---|---|
| 应用启动 | — | +<10 ms | Job 创建 + 父进程句柄监视，无网络、无磁盘枚举 |
| 正常退出（无免流） | 杀进程，瞬时 | 本地 HTTP shutdown + 等退出，<100 ms 量级 | 多一次 loopback 往返 |
| 正常退出（有免流） | 前端先「停止并退出」 | 不变 | 停止逻辑本来就是用户已等待的路径 |
| 强杀外壳后的收尾 | 无（成为孤儿） | 优雅停止通常 1–2 s；硬杀兜底瞬时 | 见 §2.2 的保证边界 |
| 更新检查最坏等待 | 单通道 6 候选 × 8 s = **48 s**；beta 客户端两通道串行最坏 **96 s** | 有界并发，总预算 **15 s** | 复用公告拉取的「并发 + 首成功即返回」模式（[notice.go:271](../../cmd/freev6-helper/notice.go#L271)） |
| 签名校验 | — | Ed25519 验签 ~50–100 µs + 每个候选并行多取一个小文件 | 峰值可忽略 |
| 前端轮询稳态 | 3 s / 1 s / 5 s | 不变 | 只给卡死的请求加超时，不改变频率 |
| app.js 拆分 | — | 运行时零成本 | 纯脚本搬迁，不引入打包器/框架 |

带数字的验收目标见 §5。

### 0.4 稳定性结论

- **能兑现的**：正常退出、停止并退出、更新安装三条主路径可以做到「先还原网络状态、再退出、无残留」；任务管理器强杀桌面壳时，健康的 helper 仍能自动完成收尾；helper 自身崩溃或被终止时，核心随 Job 退出，不再留下无主提权进程。
- **不能兑现的**：硬杀（TerminateProcess）路径无法保证网络状态 100% 还原——热点共享状态在极端情况下要等下次启动自愈；如果 helper 同时挂死，核心也可能残留到下次启动。这是操作系统边界，不是实现选择，验收标准必须按此改写。
- **风险控制**：批次 A（签名）与批次 B（生命周期）分开提交、分开发布、分开验收；生命周期新增能力全部可被旧版本忽略，混合版本逐级降级而不阻断启动。

### 0.5 推荐调整

1. **把原「批次一」拆成两批**：先做**批次 A（更新信任链）**单独发一版并观察，再做**批次 B（生命周期）**。两者风险类型不同，混在一个版本里出问题无法判断是签名还是进程收尾导致的。
2. 生命周期采用「**helper 自有 Job + 父进程死亡监视 + shutdown 端点 + 共用 StopSession + 跨进程会话锁**」，而不是「桌面壳 Job + helper 加入」。
3. 配置一致性用轻量方案，不引入修订号。
4. 前端拆分保留，但降为 P2，按模块渐进，不重写。

---

## 1. 基线与证据

### 1.1 本次实际验证

| 项 | 命令 | 结果 |
|---|---|---|
| Go 全量测试 | `go test ./...` | 通过（helper 4.3 s / mihomo 36.1 s / network 2.8 s） |
| 前端测试 | `node --test tests/*.test.js` | 32/32 通过 |
| 工作区 | `git status --short` | 干净 |
| 版本 | `web/index.html` = v0.5.6-beta.3；tauri/Cargo = 0.5.6 | 一致 |
| 线上稳定版 | `releases/latest/download/version.json` | 0.5.4，UTF-8 无 BOM |

### 1.2 已核实的问题清单

| # | 问题 | 证据 | 级别 | 归属批次 |
|---|---|---|---|---|
| 1 | 应用更新清单与安装包哈希同源，镜像可同时投毒 | [update.go:51](../../cmd/freev6-helper/update.go#L51)（镜像族）、[update.go:433](../../cmd/freev6-helper/update.go#L433)（首个可解析清单即采纳）、[update.go:471](../../cmd/freev6-helper/update.go#L471)（再用同一清单校验 SHA） | P0 | A |
| 2 | 内核清单/内核包同样无签名、同源 | [coreupdate.go:48](../../internal/mihomo/coreupdate.go#L48)、[coreupdate.go:196](../../internal/mihomo/coreupdate.go#L196)（只做格式校验）、[coreupdate.go:312](../../internal/mihomo/coreupdate.go#L312)（下载后按同一清单的 SHA 校验） | P0 | A |
| 3 | beta-channel 只镜像 `version.json`，不镜像签名 | [beta-channel.yml:49](../../.github/workflows/beta-channel.yml#L49) | P0（A 的配套） | A |
| 4 | helper 没有优雅退出路径；桌面壳退出直接杀 helper | [main.rs:982](../../src-tauri/src/main.rs#L982)、[main.rs:844](../../src-tauri/src/main.rs#L844)；helper 只有 `server.Serve`，无信号/关闭处理（[main.go:219](../../cmd/freev6-helper/main.go#L219)） | P0 | B |
| 5 | 核心是独立子进程，helper 死亡后成为无主进程 | [process.go:76](../../internal/mihomo/process.go#L76)（spawn 后只 `Wait`）；[port.go:13](../../cmd/freev6-helper/port.go#L13) 的注释也承认「任务管理器杀桌面会留下 helper」 | P0 | B |
| 6 | GUI 与 CLI 的停止顺序不同：GUI 还原热点→关 TUN→停核→清路由→删快照；CLI 停核→清路由→还原热点→删快照 | [main.go:1114](../../cmd/freev6-helper/main.go#L1114)、[main.go:1141](../../cmd/freev6-helper/main.go#L1141)、[main.go:1170](../../cmd/freev6-helper/main.go#L1170)、[main.go:1184](../../cmd/freev6-helper/main.go#L1184)、[main.go:1210](../../cmd/freev6-helper/main.go#L1210)；[cmd/freev6/main.go:243](../../cmd/freev6/main.go#L243) | P0 | B |
| 7 | 跨进程锁只覆盖 Start，不覆盖 Stop 的清理段 | [process.go:62](../../internal/mihomo/process.go#L62)、[startlock_windows.go:13](../../internal/mihomo/startlock_windows.go#L13) | P1 | B |
| 8 | `MarkVerified` 标记「当前 active」，不是本次启动的版本；启动与内核更新可交错 | [corestore.go:322](../../internal/mihomo/corestore.go#L322)、[main.go:901](../../cmd/freev6-helper/main.go#L901)、[main.go:409](../../cmd/freev6-helper/main.go#L409)（更新不持 `h.mu`） | P1 | B |
| 9 | 链式/备用配置内容用 `os.WriteFile` 原地截断写，且与启用开关分两步提交 | [chain.go:143](../../cmd/freev6-helper/chain.go#L143)、[chain.go:147](../../cmd/freev6-helper/chain.go#L147)；[cloudconfig.go:228](../../cmd/freev6-helper/cloudconfig.go#L228)、[cloudconfig.go:232](../../cmd/freev6-helper/cloudconfig.go#L232) | P1 | C |
| 10 | 重置先删文件再写 settings；settings 写失败会留下「文件没了、开关还开着」 | [main.go:471](../../cmd/freev6-helper/main.go#L471) | P1 | C |
| 11 | 前端 `api()` 与 `fetchProxies()` 都没有超时；`switchProxy` 不检查 HTTP 状态 | [app.js:184](../../web/app.js#L184)、[app.js:2638](../../web/app.js#L2638)、[app.js:2766](../../web/app.js#L2766) | P1 | D |
| 12 | 更新检查串行走镜像，最坏 48 s / 96 s | [update.go:433](../../cmd/freev6-helper/update.go#L433)、[update.go:502](../../cmd/freev6-helper/update.go#L502) | P1 | A |
| 13 | 前端缺 SHA 时仍尝试更新，文案称「跳过校验」，实际 helper 会拒绝 | [app.js:1197](../../web/app.js#L1197)、[app.js:1295](../../web/app.js#L1295) | P2 | A |
| 14 | CI 无显式权限、超时、并发；Actions 固定到 tag 而非 SHA | [ci.yml:1](../../.github/workflows/ci.yml#L1)（无 permissions/timeout/concurrency）、[ci.yml:11](../../.github/workflows/ci.yml#L11) 等 | P2 | E |
| 15 | 根级无 `AGENTS.md` / `SECURITY.md` / `CONTRIBUTING.md`；`docs/agent/` 被忽略 | [.gitignore:18](../../.gitignore#L18) | P2 | E |
| 16 | 版本一致性守卫在 CI 与 release 各写一份 | [ci.yml:45](../../.github/workflows/ci.yml#L45)、[release.yml:62](../../.github/workflows/release.yml#L62) | P3 | E |
| 17 | `app.js` 4011 行；测试仅覆盖纯函数 | [app.js](../../web/app.js)（`wc -l` = 4011）、`tests/*.test.js` 4 个文件 | P2 | D |

### 1.3 做得好的地方（不要改坏）

这些是现有工程的关键不变量，后续改动必须保持：

- **本地 API 信任边界**：per-launch Bearer token、Host/Origin 守卫、拒绝 `null` Origin（[auth.go:17](../../cmd/freev6-helper/auth.go#L17)、[auth.go:53](../../cmd/freev6-helper/auth.go#L53)）。
- **pid 文件格式**：必须保持单行十进制——Rust 内存卡片直接按 `u32` 读（[main.rs](../../src-tauri/src/main.rs)），身份信息在 `.meta` sidecar 里，不要合并。
- **settings「省略即保留」语义**：`mergeSettingsOnto` + `jsonKeyPresent` 是防止一次普通保存把链式/备用配置悄悄关掉的根因修复（[main.go:1325](../../cmd/freev6-helper/main.go#L1325)）。
- **路由清理的二次判定**：停止后重读 pid，避免删掉刚启动新会话的路由（[main.go:1184](../../cmd/freev6-helper/main.go#L1184)）；任何重构都必须保留这个语义。
- **更新镜像顺序与停滞预算**：10 s 停滞换线、3 min 硬上限、SHA 不符换线（[update.go:61](../../cmd/freev6-helper/update.go#L61)）。
- **CSP 与 capabilities 已收紧**：`script-src 'self'`、无 `shell:allow-spawn`（[tauri.conf.json:27](../../src-tauri/tauri.conf.json#L27)、[default.json](../../src-tauri/capabilities/default.json)）。
- **发布追溯**：tag/版本四处校验、CHANGELOG 提取、草稿发布、provenance（[release.yml:62](../../.github/workflows/release.yml#L62)）。

---

## 2. 原批次一设计逐项复核

### 2.1 应用更新签名

**判定：保留，补齐 8 个实现细节。**

原计划的核心（`version.json` 不变、新增 `version.json.sig`、签名原始字节、公钥内置、验签失败不回退）是对的。必须补齐：

1. **签名对象必须是上传的原始字节**。当前工作流用 `pwsh` 的 `Out-File -Encoding utf8`（PS7 无 BOM），签名步骤必须在写文件之后、上传之前执行；再加一条 CI 校验「`version.json` 首字节是 `{`」，防止换 shell 后 BOM 破坏历史验签。
2. **清单与签名必须来自同一候选**。不能从镜像 A 取清单、镜像 B 取签名；每个候选 URL 派生 `.sig`，成对获取、成对验证。
3. **验签先于解析、先于缓存**。现有 `fetchLatestManifest` 是「首个可解析的清单即采纳」，签名后应改为「首个签名有效且可解析的清单才采纳」；缓存中只允许存已验证的清单。
4. **有界并发**。签名后每个候选多一个小请求；如果照搬串行，最坏等待会从 48 s 变成 96 s。要求：候选间并发、清单与签名并发、总预算 15 s、首成功即返回（复用 [notice.go:271](../../cmd/freev6-helper/notice.go#L271) 的模式）。
5. **签名文件格式带 key id**。建议 `v1.<keyId>.<base64(64 字节签名)>`，客户端内置 `map[keyId]ed25519.PublicKey`。单 key 也能用，但换钥/丢钥会让更新通道永久失效；key id 的成本只有几行解析代码。
6. **`verifyUpdateSource` 复用已验证清单**。它是下载前的第二道闸（[update.go:471](../../cmd/freev6-helper/update.go#L471)），当前会串行拉 stable + beta 两路（最坏 96 s，且发生在用户点更新之后）。改为：先查已验证缓存；stable 不匹配再查 beta；总预算 10 s。
7. **beta 指针同步必须成对、且清单最后上传**。先传 `.sig` 再传 `version.json`；上传后下载回来验一次；同步失败要让工作流失败（可 `workflow_dispatch` 修复），绝不能留下「新清单 + 旧签名」的组合。
8. **过渡与恢复写进发布文档**。包括：私钥 Secret 缺失时 release 必须失败；私钥备份方式；轮换步骤（先发含新公钥的客户端，再换签名钥）；「首个签名版本发布前，新客户端不接受未签名更新」的预期行为。

**兼容性**：旧客户端只 GET `version.json`，不会因为多一个资产而改变；新客户端对未签名发布失败关闭是有意的安全边界。

**性能**：每次检查多一到两个 1 KB 级请求；并发后最坏等待反而从 48/96 s 降到 15 s。验签为微秒级。

**不采纳的部分**：原计划「stable 与 beta 共用一把密钥、本批不处理 f6-core」中的后半句。核心清单签名见批次 A4。

### 2.2 生命周期统一

**判定：需重设计。** 原计划的目标（统一停止、异常收尾、进程树归属）正确，但机制选择不成立。

#### 先确认产品语义

- 托盘退出的弹窗只有「取消 / 停止并退出」，没有「直接退出」按钮（[index.html:664](../../web/index.html#L664)）；`quitDirectly` 只在免流未运行时可达（[app.js:987](../../web/app.js#L987)、[app.js:999](../../web/app.js#L999)）。
- 用户文档明确要求「正常退出前先停止免流」（外部文档站 `guide/usage.md`，「退出与恢复」）。
- 因此「退出 = 停止免流」是既定产品语义；强杀/崩溃路径今天会留下无主核心，属于缺陷而不是特性。

#### 原方案的三个硬伤

1. **安全启动依赖**：新 helper 必须加入桌面壳创建的 Job，否则「明确失败」。旧壳不会创建该 Job，混合版本直接不可用。现场已经出现过混合构建（[main.rs:150](../../src-tauri/src/main.rs#L150) 描述了 0.5.4 壳 + 0.5.2 helper 的后果），不能再用启动强依赖放大它。
2. **强杀时序**：`KILL_ON_JOB_CLOSE` 在桌面壳消失的瞬间终止 helper 与核心，helper 没有机会「先还原热点、再关 TUN」。热点共享会停在一个指向已死 TUN 的状态，热点客户端断网，直到用户下次启动触发自愈。
3. **保证边界不清**：原验收写「强杀外壳后无残留路由或热点共享状态」，但硬杀路径只能保证「没有提权进程残留」；路由会随 wintun 适配器消失被系统回收，热点状态只能下次启动自愈。承诺必须按能力改写。

#### 修正后的所有权模型

```
正常退出 / 更新安装 / 退出确认
  桌面壳 ──POST /api/v1/shutdown（Bearer）──▶ helper
                                             ├─ StopSession（共用流程）
                                             ├─ 响应 200
                                             └─ http.Server.Shutdown → 进程退出
桌面壳随后等待 HELPER_TERMINATED（有界）；超时则 taskkill 兜底

桌面壳被强杀 / 崩溃
  helper 的父进程监视（等待父进程句柄）触发
                                             ├─ StopSession（有界）
                                             └─ 退出
helper 崩溃 / 被 taskkill
  helper 自有的 Job（KILL_ON_JOB_CLOSE）关闭
                                             └─ 核心与临时子进程（powershell 等）一并退出
```

要点：

- **Job 由 helper 创建并持有**，helper 自身加入该 Job（Windows 8+ 支持嵌套 Job；失败则退化为「核心 spawn 后显式 AssignProcessToJobObject」）。这样 helper 不需要桌面壳配合就能保护进程树，混合版本不受影响。
- **父进程 PID 通过环境变量传入**（`FREEV6_PARENT_PID`）。旧壳不传 → 监视器不启用；旧 helper 不认 → 忽略。两端都可独立升级。
- **shutdown 接口失败关闭**：`POST` + Bearer；旧 helper 返回 404/405 时桌面壳回落到 `/proxy/stop` + 杀进程。
- **更新安装门禁**：只有确认 helper 退出（`HELPER_TERMINATED` 或进程消失）才启动 NSIS；否则中止并保留当前版本。`kill_stray_helpers`（[main.rs:692](../../src-tauri/src/main.rs#L692)）保留，用于清理历史混合构建。
- **NSIS 安装器不受 Job 约束**：它由桌面壳 spawn（[main.rs:181](../../src-tauri/src/main.rs#L181)），不是 helper 的子进程。

#### 共用 StopSession 的顺序（GUI / CLI / 退出共用）

严格保持 GUI 现有顺序，因为「还原 ICS 共享角色要求 FreeV6TUN 仍然存在」：

1. 载入快照；还原热点/ICS（预算沿用 `icsOpTimeout`，最长 120 s）；
2. 关 TUN（PATCH `/configs`）并等适配器消失（`tunStageTimeout` 4 s）；
3. 停止核心并等待退出（`stopExitTimeout` 2 s）；
4. **重读 pid**，确认没有新会话后清残留路由（`tunCleanupTimeout` 3 s）；
5. 仅当「快照有效、热点还原成功、且没有新会话」才删除快照。

第 2–5 步放在同一个跨进程会话锁内，锁持有时间有界（≈9 s + 进程退出等待）。热点还原放在锁外（它可能 120 s，不能阻塞 CLI 启停），但快照删除必须回锁内做二次判定。

失败策略保持不变：热点还原失败仍继续停止核心，但保留快照供下次启动自愈；核心停止失败返回错误，不清路由。

#### 会话语义锁

- 复用现有 Win32 命名互斥量**对象名** `Local\FreeV6-MihomoStart`（[startlock_windows.go:13](../../internal/mihomo/startlock_windows.go#L13)）：名字是跨版本 ABI，改了会让新旧二进制互不互斥。只把 Go 标识符/文件改名为 `sessionlock_*`，对象名不变。
- 覆盖范围：Start 的「检查→spawn→写 pid」维持现状；Stop 增加「重读→停核→清路由→删快照」临界区。
- 等待从 `INFINITE` 改为有界（建议 60 s）并返回可读错误，避免持有者挂死时永久卡住 UI。
- 兼容性代价：窗口期极短的混合版本（新 CLI + 旧 helper）仍共享同一个对象名，因此互斥照常生效。

#### MarkVerified 竞态

- `MarkVerified(version string) bool`：仅当 `index.Active` 对应的版本等于本次**实际启动**的版本才写入 `VerifiedAt`，返回是否标记成功。
- 只有返回 `true` 才调用 `Prune()`（[main.go:901](../../cmd/freev6-helper/main.go#L901)）。否则跳过：说明启动期间 active 已被更新切换，不能把未跑过的新内核标成已验证，也不能按新 active 清理旧版本。
- 补充：`Prune` 在删除失败（例如旧核心正在运行、Windows 不允许删 exe）时不应清空 `Previous` 指针；只有确认文件已删除才清。

#### 能保证什么、不能保证什么

| 场景 | 保证 | 不保证 |
|---|---|---|
| 正常退出（无免流） | helper 优雅退出，无残留 | — |
| 正常退出（有免流，先停止） | 网络状态还原，无残留 | — |
| 直接杀桌面壳 | helper 在数秒内完成 StopSession；无核心/helper 残留；热点通常可还原 | helper 同时挂死时，核心可能残留到下次启动；下次启动自愈 |
| 杀 helper | Job 关闭，核心与临时子进程退出；无无主核心 | 网络状态（热点）留待下次启动自愈 |
| helper 挂死 + 正常退出壳 | 壳有界等待后 taskkill → Job 关闭 → 核心退出 | 热点状态留待下次启动自愈 |
| 更新安装 | helper 确认退出后才启动 NSIS，不再产生混合构建 | — |

#### 兼容性矩阵（生命周期）

| 桌面壳 | helper | 行为 |
|---|---|---|
| 新 | 新 | shutdown + 父进程监视 + Job，全量能力 |
| 新 | 旧 | shutdown 404 → `/proxy/stop`（有界）+ 杀进程；无 Job（旧行为）；更新门禁更严格 |
| 旧 | 新 | 旧壳直接杀；父进程监视不启用（无 env）；Job 仍保护核心；接口全兼容 |
| 旧 | 旧 | 与今天完全一致（回退基线） |

### 2.3 配置一致性

**判定：换轻量方案，不引入修订号。**

真实缺陷只有三点：

1. 内容文件是原地截断写（[chain.go:143](../../cmd/freev6-helper/chain.go#L143)、[cloudconfig.go:228](../../cmd/freev6-helper/cloudconfig.go#L228)），并发读者可能读到半截 YAML；
2. 内容与启用开关分两步提交，失败时状态可能「文件新、开关旧」或反之；
3. reset 先删文件后写 settings（[main.go:471](../../cmd/freev6-helper/main.go#L471)），写失败会留下「文件没了、开关还开着」。

修正：

- 内容写入改成 `writeFileAtomic`（临时文件 + rename；仓库已有该函数）；
- 新增 `configMu`：导入的「写内容 + 写 settings」在同一临界区；启动读配置时也取同一把锁，保证拿到一致的「内容 + 开关」快照；
- 导入失败补偿：settings 写失败时尝试恢复旧内容；恢复失败只记日志，开关保持原值（用户可重试）；
- reset 顺序反转：先把 settings 写成默认（开关全关），再删内容文件；残留文件在开关关闭时不可达，且下次导入会覆盖；
- **不引入**修订号、expectedRevision、目录 fsync——本仓库没有多写者场景。

### 2.4 仓库规则与 CI

**判定：保留，优先级下调到 P2；不做形式主义检查。**

保留项：

- CI 增加顶层 `permissions: contents: read`、每个 job `timeout-minutes`、`concurrency`（取消同分支旧跑）；
- Actions 固定到提交 SHA + Dependabot 自动升级；
- 版本一致性守卫抽成一个脚本，CI 与 release 共用（现在两处各写一份，[ci.yml:45](../../.github/workflows/ci.yml#L45) / [release.yml:62](../../.github/workflows/release.yml#L62)）；
- 新增根级 `AGENTS.md`（构建/测试/架构不变量/发版步骤）、`SECURITY.md`（私下报告漏洞）、最小 `CONTRIBUTING.md`；
- 设计文档进版本控制（`docs/design/`），`docs/agent/` 继续作为本地审查区。

不采纳项：

- 为「仓库结构检查」再造一个 `check-repository.py`：本仓库没有第三方源码目录、没有多平台树，能检查的东西（版本、图标子集、法律文件）已经各有守卫。只补真正会回归的两条：CSP 不得回到 `null`、capabilities 不得重新出现 `shell:allow-spawn`。
- 文档站并入本仓库：文档站是独立 Vercel 项目，合并只会扩大发布面。

### 2.5 前端

**判定：保留目标，改为分阶段；明确不重写、不引依赖。**

- 先修行为韧性（超时、状态检查），再拆文件。拆分顺序：更新流程 → API 客户端 → 轮询生命周期 → 节点/渲染纯函数。
- 拆分只做「同文件搬移 + 明确挂载点」（UMD/经典脚本，保持现有加载顺序 [index.html:807](../../web/index.html#L807) 之后），不引入打包器、框架、npm 依赖。
- 测试：Node 内置 `node:test` + 注入 fake fetch / fake timers，测试 API 重试、超时、轮询门控、候选选择等纯逻辑；**不引入 jsdom/Playwright CI**（避免依赖膨胀），DOM 行为用浏览器手测核对。

### 2.6 原计划文件清单的处置

| 原计划文件 | 处置 | 说明 |
|---|---|---|
| `internal/appupdate/verify.go`、`verify_test.go`、`update_public.pem` | **保留** | 增加 `keys.go`（key id → 公钥映射） |
| `cmd/freev6-update-sign/main.go`、`main_test.go` | **保留** | 增加 `-verify` 模式、原子写 |
| `internal/lifecycle/stop.go`、`stop_test.go` | **保留** | 依赖注入以便测顺序/失败路径 |
| `src-tauri/src/job_object.rs` | **删除** | Job 改由 helper 创建并持有，Rust 不引入 Job API；避免「新 helper 必须加入壳的 Job」 |
| `src-tauri/src/helper_client.rs` | **保留** | 用 `std::net::TcpStream` 手写最小 HTTP/1.1，不加 reqwest 依赖 |
| `cmd/freev6-helper/update.go`、`update_test.go` | **保留** | 成对取清单+签名、有界并发、缓存只存已验证 |
| `cmd/freev6-helper/main.go`、`main_test.go` | **保留** | 增加 shutdown 端点、父进程监视、Job 生命周期 |
| `cmd/freev6/main.go` | **保留** | 停止改走共用 StopSession（补关 TUN） |
| `internal/mihomo/process.go` | **保留** | Job 挂接点 + 会话锁复用 |
| `internal/mihomo/startlock_windows.go` → `sessionlock_windows.go`、`startlock_unix.go` → `sessionlock_unix.go` | **改名保留** | **Win32 对象名 `Local\FreeV6-MihomoStart` 不变**（跨版本互斥 ABI） |
| `src-tauri/src/main.rs` | **保留（修正）** | 保留 shutdown 调用与更新门禁；删除「桌面壳创建 Job」部分 |
| `src-tauri/Cargo.toml`、`Cargo.lock` | **按需微调** | Job 移入 Go 后不需要新的 Rust 依赖；`windows-sys` 现有 features 已够内存卡片用 |
| `web/app.js` | **保留** | 移除「未附 SHA 跳过校验」分支；超时/状态检查另行分批 |
| `.github/workflows/release.yml`、`beta-channel.yml`、`docs/release.md` | **保留** | 增加签名、上传顺序、上传后验签、密钥运维 |
| 新增 `internal/mihomo/job_windows.go`、`job_other.go`、`job_windows_test.go` | **新增** | helper 自有 Job 的创建、加入、核心挂接 |
| 新增 `docs/design/` | **新增** | 设计文档纳入版本控制（本文即第一篇） |

---

## 3. 重规划批次

### 批次 A — 更新信任链（P0，建议单独发布）

**目标**：镜像被攻破时，无法让客户端安装任意安装包或内核。  
**非目标**：不做安装包 Authenticode 签名（SmartScreen 另案）；不改更新 UI 流程。

#### A1 · 签名格式与工具

- 新增 `internal/appupdate/`：
  - `keys.go`：`//go:embed update_public.pem` + `map[keyId]ed25519.PublicKey`；
  - `verify.go`：`VerifySignature(manifestBytes, sigText []byte) error`，解析 `v1.<keyId>.<base64>`，校验长度与签名；
  - `verify_test.go`：有效签名、篡改一字节、未知 keyId、截断、空文件、错误长度。
- 新增 `cmd/freev6-update-sign/main.go`：
  - `-in version.json -out version.json.sig -key-id 2026-11 -key-file key.pem`；
  - `-verify` 模式供工作流上传后复核；
  - 原子写输出；私钥缺失/格式错误退出非零。
- 新增 `internal/appupdate/update_public.pem`（公钥进仓库，私钥只进 GitHub Secret）。

#### A2 · helper 集成（应用更新）

修改 `cmd/freev6-helper/update.go`：

- `fetchLatestManifest` → `fetchVerifiedManifest`：候选间并发；每个候选成对取 `version.json` 与 `version.json.sig`；总预算 15 s；首个验签通过且可解析者胜出；失败关闭。
- `updateLatest`：stable 与 beta 两路并发（仅 beta 客户端请求两路）；任一成功即可返回；把 `betaError` 保留给前端日志。
- `verifyUpdateSource`：只使用已验证清单缓存；先 stable 后 beta，总预算 10 s；不再每次串行重拉两路。
- 缓存结构增加「已验证」语义；未验证内容不得写入缓存。

测试（扩展 `update_test.go`）：

- 签名有效 → 接受；无签名/签名不符/未知 key → 拒绝；
- 镜像 A 返回清单、镜像 B 返回签名 → 拒绝（必须同候选）；
- 清单与签名分别来自篡改/未篡改组合 → 拒绝；
- 一条镜像挂起 → 15 s 内返回且不阻塞成功线路；
- beta 同步缺失签名 → beta 候选被拒，stable 不受影响；
- `verifyUpdateSource`：stable 命中不请求 beta；stable 未命中才请求 beta；缓存过期后重新验证。

#### A3 · 发布工作流

- `release.yml`：生成 `version.json` 后调用签名工具；Secret `FREEV6_UPDATE_SIGNING_KEY` 缺失则失败；先把 `.sig` 上传、再上传 `version.json`；attestation 增加 `.sig`；发布后下载资产复核签名。
- `beta-channel.yml`：下载 `version.json` + `.sig`；先传 `.sig`、再传 `version.json`；上传后拉回验签；任一步失败让工作流失败。
- `docs/release.md`：密钥生成/备份/轮换、Secret 配置、同步失败修复命令、首个签名版本的过渡说明。
- 前端 [app.js:1197](../../web/app.js#L1197) / [app.js:1295](../../web/app.js#L1295)：移除「未附 SHA 跳过校验」分支；缺 SHA 直接报错，不发起下载。

验收：

- 模拟镜像返回「恶意安装包 + 与之匹配的清单」→ 客户端拒绝（因为清单无有效签名）。
- 用 0.5.4 旧客户端实测可升级到首个带签名版本。
- 故意不配置 Secret → 发布工作流失败，不产生未签名新版。
- beta 同步中断 → 旧 beta 指针仍可用；新客户端提示无更新而非回退未签名。

回滚：签名是纯增量资产；代码回滚只能由「已带验签的客户端」之后的版本执行，且必须仍然提供有效签名清单。密钥丢失的恢复见发布文档。

#### A4 · 内核清单签名（跨仓库）

- `f6-core` 仓库的发布流程生成 `core.json.sig`（同一 key id 或独立 key id，需确认归属）；
- `internal/mihomo/coreupdate.go`：`FetchCoreManifest` 取清单时成对取签名并验签；缓存只存已验证清单；验签失败回退过期缓存（缓存本身是上次已验证的）或报错；
- 测试：有效/篡改/缺签名/未知 key/缓存回退；
- 验收：模拟镜像替换 `core.json` 与内核包 → 拒绝安装。

#### A5 · 更新检查时延

与 A2 合并实施：stable/beta 并发、候选并发、总预算 15 s。用本地挂起 server 测试「一条线路 8 s 不回包」场景，断言总耗时 ≤ 15 s。

**批次 A 工作量估算**：2–3 个工作日 + 一次真实发布验证。

---

### 批次 B — 生命周期与会话所有权（P0）

**目标**：正常退出、强杀、更新安装、CLI 停止都走同一套收尾语义；不留无主提权进程。  
**非目标**：不做 Windows 服务化；不做 Linux/macOS 的 Job 等价物。

#### B1 · 共用 StopSession

- 新增 `internal/lifecycle/stop.go`：`StopSession(ctx, Options) (Result, error)`；
  - `Options` 注入：`LoadSnapshot / RestoreHotspot / DisableTun / WaitAdapterGone / StopCore / ClearRoutes / ReadStatus`（默认实现指向现有函数，测试注入假实现）；
  - 顺序严格按 §2.2；
  - `Result` 携带各阶段耗时、restoreError、restoreSkipped、clearedRoutes、exited。
- `cmd/freev6-helper/main.go` 的 `stopProxy` 改为调用该模块；
- `cmd/freev6/main.go` 的 `stopMihomo` 改为调用该模块（新增读取 controller secret 以支持关 TUN）；
- 测试 `internal/lifecycle/stop_test.go`：
  - 正常顺序断言（还原→TUN→停核→重读→清路由→删快照）；
  - 还原失败 → 仍停核、保留快照；
  - 停核失败 → 返回错误、不清路由；
  - 停止期间新核心已启动 → 跳过清路由与删快照；
  - 适配器未消失 → 仍继续停核并记录。

#### B2 · 会话锁

- `internal/mihomo/startlock_*` 标识符改名 `sessionlock_*`，**Win32 对象名不变**；
- Start 维持现有临界区；Stop 的「重读→停核→清路由→删快照」进入同一锁；
- 等待有界（60 s），超时返回明确错误；
- 测试：同进程并发 start/stop（模拟）+ Windows 命名互斥跨进程手测。

#### B3 · shutdown 端点与父进程监视

- helper 新增 `POST /api/v1/shutdown`（Bearer；可选 `?budget=fast`）：
  - 取 `h.mu`，若核心在跑则执行 StopSession；响应 200 + 结构化结果；
  - 异步 `http.Server.Shutdown` + 进程退出；用 `sync.Once` 防止与父进程监视重复执行。
- helper 读取 `FREEV6_PARENT_PID`：打开父进程句柄 `SYNCHRONIZE`，等待退出 → 触发同一收尾并退出；父 PID 无效时禁用（旧壳兼容）。
- 测试：端点鉴权/方法；StopSession 只调用一次；父进程监视用 Windows 子进程模拟（CI Windows job）。

#### B4 · helper 自有 Job

- `internal/mihomo/job_windows.go`：
  - `NewKillOnCloseJob()`；`AssignCurrentProcess()`；失败返回可记录错误；
  - 若自加入失败（已有不允许嵌套的 Job）→ 在 `Start` 后对核心进程 `AssignProcessToJobObject`；
- helper 启动时创建 Job 并自加入；非 Windows 提供空实现；
- 测试：Windows 测试创建 Job，spawn `cmd /c timeout 30`，关闭 Job 后断言进程退出（CI Windows job；失败重试一次避免抖动）。
- 说明：此改动使「杀 helper」会带走核心；属于有意行为变化（见 §6）。

#### B5 · MarkVerified 版本精确

- `MarkVerified(version string) bool`；helper 启动路径传入 `core.Version`；
- 仅标记成功时 `Prune()`；
- `Prune` 只有在相关文件确认删除后才清 `Previous`；测试覆盖「删文件失败（模拟）→ Previous 保留」。

#### B6 · 桌面壳退出与更新门禁

- 新增 `src-tauri/src/helper_client.rs`：用 `std::net::TcpStream` 手写最小 HTTP/1.1（loopback、无新依赖），支持超时；
- `update_apply`：先 stop（shutdown，预算 15 s；404 回落 `/proxy/stop`）→ 等待 helper 退出（15 s）→ 未退出则中止更新并提示；确认退出后 `kill_stray_helpers` → 启动 NSIS；
- `RunEvent::Exit`：调用 shutdown（预算 15 s）后 kill 兜底；
- 单元测试：HTTP 响应解析（200/404/超时）；更新门禁的超时分支（不真正启动 NSIS）。
- 兼容：旧 helper 404 → 回落；`HELPER_TERMINATED` 机制保留。

#### B7 · 孤儿接管的等待

- `listenOrTakeOver`（[port.go:20](../../cmd/freev6-helper/port.go#L20)）：发现孤儿 helper 时，先等端口释放（建议 8 s），仍占用才 kill；避免把正在优雅收尾的旧 helper 杀掉，导致热点还原半途而废；
- 测试：模拟「孤儿正在退出」→ 不 kill，接管成功；「孤儿挂死」→ 超时后 kill。

#### B8 · CLI 停止对齐

- 用共用模块后 CLI 自动获得：先还原热点、再关 TUN、再停核；
- 保留 CLI 的输出与退出码语义；`freev6 version` 目前硬编码 `0.1.0`（[cmd/freev6/main.go:43](../../cmd/freev6/main.go#L43)）——顺手改为读取构建信息或明确标注诊断工具版本（P3，可选）。

**批次 B 验收矩阵（Windows 真机）**：

| 场景 | 预期 |
|---|---|
| 停止并退出（无热点） | ≤2 s 完成；无核心/helper；快照删除 |
| 停止并退出（热点共享） | 热点恢复到原上游；快照删除 |
| 任务管理器结束桌面壳 | helper 数秒内收尾；无核心/helper；热点通常恢复；再次启动正常 |
| 任务管理器结束 helper | 核心随 Job 退出；再次启动触发残留自愈 |
| 结束核心进程 | helper 状态变为未运行；可重新启动 |
| 免流中执行更新 | 先停止，再确认 helper 退出，NSIS 启动；无混合构建 |
| CLI stop | 与 GUI 相同顺序；不再出现「CLI 停完网络恢复更慢」 |
| 旧壳 + 新 helper / 新壳 + 旧 helper | 见 §2.2 矩阵 |

**批次 B 工作量估算**：3–4 个工作日 + 1 天真机验收。

---

### 批次 C — 配置一致性（P1）

- C1：`writeFileAtomic` + `configMu` + 补偿（chain/cloud 导入）；
- C2：reset 顺序反转（先 settings 后删文件）；
- C3：start 读配置时取 `configMu` 快照（内容 + 开关）；
- 测试：并发导入 + 启动；注入 settings 写失败；注入 rename 失败；半截文件读者；
- 验收：任何失败路径下，下一次启动都能用「旧内容 + 旧开关」或「新内容 + 新开关」正常工作，不出现「开关开、内容坏」。

工作量：约 1 个工作日。

---

### 批次 D — 前端韧性与可维护性（P1/P2）

- D1 `api()` 分层超时：默认 15 s；`/proxy/start`、`/proxy/stop`、`/update/download` 180 s；progress/status 10 s；保留 401 重试一次；
- D2 controller 直连（9090）统一 `AbortSignal.timeout(3–5 s)`；`switchProxy` 检查 `resp.ok`，失败回滚 `state.proxyGroup.now` 并提示；
- D3 拆分（每步独立提交、纯搬移）：
  1. 更新流程 → `web/update.js`（检查/候选选择/下载轮询）；
  2. API 客户端 → `web/lib/api.js`（token、端口回落、401 重试、超时）；
  3. 轮询生命周期 → `web/lib/poll.js`（注册/可见性暂停/去重）；
  4. 节点与图表纯函数 → `web/lib/nodes.js` / 现有 `traffic.js`；
- D4 测试：候选选择（stable/beta/降级）、API 超时与重试、轮询门控；`node --test`，不引新依赖；
- D5 浏览器手测：启动/停止/更新检查/弱网（DevTools offline、慢请求）/页面隐藏恢复。
- 非目标：不改 CSS、不改 index.html 结构、不引入构建工具。

工作量：2–3 个工作日（可分两个版本做）。

---

### 批次 E — 工程门禁与仓库规范（P2）

- E1 CI：`permissions: contents: read`、job `timeout-minutes`、`concurrency` 取消旧跑；Actions 固定 SHA；新增 `.github/dependabot.yml`（github-actions / gomod / cargo）；
- E2 版本守卫脚本化：`scripts/check-version.ps1`，CI 与 release 共用；CI 增加 actionlint（固定版本二进制）；
- E3 新增根级 `AGENTS.md`（构建/测试/不变量/发版）、`SECURITY.md`、`CONTRIBUTING.md`；
- E4 把「签名资产存在且可验证」加入发布检查清单；设计文档放 `docs/design/` 并纳入版本控制；
- 可选：CI 增加两条不变量检查——`tauri.conf.json` 的 CSP 不得为 `null`；capabilities 不得含 `shell:allow-spawn`。

工作量：0.5–1 个工作日。

---

## 4. 全量兼容性矩阵

| 维度 | 兼容策略 | 验证方式 |
|---|---|---|
| 旧客户端升级到签名版 | `.sig` 是附加资产；旧代码路径不变 | 用 0.5.4 安装包实测升级 |
| 新客户端遇到未签名发布 | 失败关闭，视为「无更新」，记录错误 | 单测 + 模拟镜像 |
| 新 helper + 旧桌面壳 | shutdown 只是附加路由；父 PID 缺失则不监视；Job 在 helper 内部 | 手动互换二进制启动 |
| 旧 helper + 新桌面壳 | shutdown 404 → `/proxy/stop`；更新门禁在 helper 未退出时中止 | 手动互换二进制 + 更新演练 |
| 混合构建残留 | `kill_stray_helpers` 保留；混合版 UI 端口回落逻辑不动 | 手工制造混合构建 |
| CLI 新旧混用 | Win32 互斥对象名不变；CLI 参数不变 | 手测并发 start/stop |
| settings 文件 | 只新增字段；「省略即保留」不变 | 现有测试 + 手测 |
| 链式/备用内容文件 | rename 原子替换；旧文件可读 | 并发导入测试 |
| pid 文件 | 单行格式不变；身份仍在 `.meta` | Rust 内存卡片手测 |
| NSIS 安装器 | 不属于 helper 的 Job；更新门禁保证文件不被占用 | 更新安装演练 |
| 非 Windows 构建 | Job/父进程监视为空实现；CLI 逻辑共用 | `go build` + 单测 |

---

## 5. 性能预算与测量

| 指标 | 目标 | 测量方式 |
|---|---|---|
| helper 启动增量 | <10 ms | 在 helper 启动处打点，对比 `helper.log` |
| 正常退出（无免流） | <100 ms 额外 | 前端日志/壳侧计时 |
| StopSession 总耗时 | 与现状持平（典型 1–2 s；热点还原失败最坏 120 s） | helper.log 已有的阶段耗时行 |
| 更新检查（stable） | 最坏 ≤15 s（现状最坏 48 s） | 本地挂起镜像测试 |
| 更新检查（beta 客户端） | 最坏 ≤15 s（现状最坏 96 s） | 同上 |
| 验签 | ≤1 ms | 基准测试 |
| 签名后下载启动延迟 | 不增加（验证与清单同批） | 同上 |
| 前端稳态请求数/频率 | 不变 | DevTools Network |
| app.js 拆分后首屏 | FCP/DCL 不劣化（2026-11-27 实机基线：FCP 176–208 ms / DCL 393–448 ms） | Chrome Performance 手测 |

原则：任何需要「多等一次网络」的改动，都必须有明确预算并在失败时快速返回；正常路径不允许新增串行等待。

---

## 6. 需要拍板的决策

| # | 决策 | 推荐 | 理由 |
|---|---|---|---|
| 1 | helper 被终止时，核心是否随 Job 退出？ | **是** | 无主核心无 UI 可控制；退出语义已经是「先停止」；Job 是唯一能保证不残留提权进程的机制 |
| 2 | 桌面壳被强杀时，helper 是否自动收尾？ | **是** | 用户文档承诺「退出软件尽量恢复网络」；父进程监视能在硬杀时仍完成优雅收尾 |
| 3 | 更新安装遇到 helper 未确认退出 | **中止安装** | 继续安装会制造混合构建，比「更新失败」更难恢复 |
| 4 | 签名格式是否带 key id | **是** | 换钥/丢钥不会让更新通道永久失效；实现成本仅几行 |
| 5 | 核心清单签名是否本周期做 | **是（独立小批次，跨仓库）** | 风险与应用更新同级；推迟没有安全收益 |
| 6 | 前端拆分深度 | **分阶段，第一批只做更新流程 + API 客户端** | 控制风险；不引入打包器 |
| 7 | 是否引入 jsdom/Playwright CI | **否** | 依赖与维护成本高于收益；纯逻辑用 Node 内置测试，DOM 手测 |

---

## 7. 实施顺序与门禁

1. **批次 A** → 发布一个带签名能力的版本 → 真机验证旧客户端升级、签名拒绝、beta 同步；观察一个发布周期。
2. **批次 B** → 先单测/集成，再 Windows 真机矩阵（杀壳、杀 helper、更新、热点、CLI）。
3. **批次 C** → 与 B 可并行的低风险项，单独提交。
4. **批次 D** → 每个拆分步骤单独提交，浏览器手测。
5. **批次 E** → 随任意批次合入，不阻塞发布。

每批开始前：列出逐文件改动与验证命令 → 征求确认 → 实施 → 真机/自动化验收 → 英文提交。每批结束更新 `CHANGELOG.md`（若随版本发布）与 `docs/release.md`（若涉发布流程）。

---

## 附录：复核命令

```bash
go test ./...
node --test tests/*.test.js
gofmt -l cmd internal
go vet ./...
git status --short
```

关键证据文件：

- 更新：`cmd/freev6-helper/update.go`、`internal/mihomo/coreupdate.go`、`.github/workflows/release.yml`、`.github/workflows/beta-channel.yml`
- 生命周期：`cmd/freev6-helper/main.go`、`cmd/freev6/main.go`、`internal/mihomo/process.go`、`internal/mihomo/shutdown.go`、`internal/mihomo/startlock_*.go`、`src-tauri/src/main.rs`
- 配置：`cmd/freev6-helper/chain.go`、`cmd/freev6-helper/cloudconfig.go`
- 前端：`web/app.js`、`web/index.html`、`tests/*.test.js`
- 门禁：`.github/workflows/ci.yml`、`.gitignore`、`docs/release.md`