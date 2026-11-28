# FreeV6 改进计划 v2（2026-11-28）

> 复核对象：`docs/design/2026-11-28-improvement-plan.md`（下称 v1）。
> 代码基线：`0bc4cc0`（工作区仅 `docs/design/` 未跟踪）。本轮**只做复核与重规划，未改任何产品代码**。
> 本轮实测：`go test ./...` 通过（helper 4.428s / mihomo 36.094s / network 2.907s，墙钟 39.2s）、`node --test tests/*.test.js` 32/32 通过（150ms）、`go vet ./...` 通过、`gofmt -l cmd internal` **28 个文件非空**（见 §1.2，v1 未发现）。
> 线上 `version.json` 本轮已实拉（0.5.4，首字节 `{`，无 BOM，正文含 CRLF）；最新 release 仅两份资产（setup.exe、version.json），**尚无 `.sig`**。
> **基线实测（2026-11-28，批次 1 改动前）见 §1.4**：helper 冷启动 p50 34ms、`/update/latest` 健康网络 ~1.0s、`/runtime/check` ~1.1s。批次 1 的验收以这些数字为对照基准。

---

## 0. 一页结论

### 0.1 对 v1 的总体判定

**方向正确、结构可用，但不能按原样执行。** v1 的问题清单（17 项）经复核全部成立，证据链接准确率很高；"签名失败关闭"、"Job 归 helper"、"不引入修订号"、"前端不引打包器"这几个关键取舍也对。真正的问题不在"识别了什么"，而在**三处系统性缺口**：

| 维度 | 评价 | 说明 |
|---|---|---|
| 问题识别 | 强 | 17 项全部可复现，行号可核 |
| 方案取舍 | 较强 | 5 处设计级修正（Job 归属、验收边界、MarkVerified、内核签名、不引修订号）都成立 |
| 性能论证 | **弱** | 只给了"改后"数字，没给**每个新增等待的上界**；有 5 处会在冷启动/退出路径上引入未封顶或上界过大的等待（§5.2） |
| 跨批次一致性 | **弱** | D1 的"默认 15s 超时"会直接砍掉 A2 的 15s 预算与 `/runtime/check` 的 20s 预算；分批实施会互相打脸（§2.2.2） |
| 实现可执行性 | **中** | 缺 7 处必须补齐的设计/实现约束（§2.2），其中最严重的两个是 `http.Server.Shutdown` 无界、会话锁有界等待忽略返回值 |
| 验证方案 | 中 | 验收写的是"场景"，不是"可测指标+观测点"；§6.5 已补成可执行脚本 |

### 0.2 v1 必须修正的 7 处（详见 §2.2）

1. `updateLatest` 不回填已验证清单缓存 → 用户点"立即更新"后仍要重拉两路清单，最坏 **96s 才开始下载**。
2. 前端 `api()` 一律 15s → 会把 `/update/latest`（预算 15s）与 `/runtime/check`（预算 20s）砍成"无更新"。
3. 孤儿接管"先等 8s" → 直接加在**启动关键路径**上。
4. 更新门禁 15s + 15s → 用户点更新后最多干等 30s。
5. `RunEvent::Exit` 里做 15s HTTP 等待 → 退出可能"卡住不动"。
6. shutdown 端点未定义边界：`http.Server.Shutdown` 无超时、客户端断开是否取消收尾未定义。
7. 会话锁改有界等待后，必须检查 `WaitForSingleObject` 的**返回码**（现实现丢弃），否则 `WAIT_TIMEOUT` 会被当成"拿到锁"。

### 0.3 v1 漏掉的 6 处（详见 §2.3）

| # | 漏项 | 后果 |
|---|---|---|
| 1 | 启动期 `kill_stray_helpers()` 无条件先杀 | 打断上一实例 helper 的优雅收尾（与 B7 同源，但发生在壳侧，v1 只处理了 helper 侧） |
| 2 | 备用配置的公告门禁依赖 7 天缓存 | 离线/镜像全挂超过 7 天 → 已导入的备用配置**无法启动**（400 cloud-locked） |
| 3 | `fetchProxies()` 无超时 + `proxiesPolling` 单飞标志 | 9090 半死一次，节点列表**永久冻结**到刷新页面 |
| 4 | `update_apply` 改优雅关停后未先置 `stopping` | helper 正常退出会被当成异常退出 → 弹"组件异常"并 `exit(1)` |
| 5 | 前端拆分对象是**经典脚本** | 裸搬移会让同名顶层 `const` 在第二个文件里 `SyntaxError`，整段脚本不执行（静默半残） |
| 6 | 本地 `gofmt` 门禁与 `core.autocrlf=true` 冲突 | 实测 28/96 文件永久"脏"，CI 绿、本机红，门禁形同虚设 |

### 0.4 首批范围

**批次 1 = 更新信任链 + 时延（v1 的批次 A，按其修正意见执行）**，含发布流水线与一次 beta 实机验证；**不含**生命周期（批次 2）、配置（批次 3）、前端拆分（批次 4）。理由：A 的爆炸半径最小（验签失败关闭，不改进程语义）、可本地单测覆盖、且能把最坏 48/96s 的等待压到 15s 以内。逐文件清单与实机脚本见 §6。

**当前状态：已实施（2026-11-28）。** 改动前基线（§1.4）与改动后验证（`2026-11-28-batch1-test-record.md`）已配对。T3–T9 的现实验证要等一次签名 beta 发布，当前阻塞在仓库公钥与 GitHub Secret（见 §6.8）。

---

## 1. 复核基线与本轮实测

### 1.1 本轮实际执行

| 项 | 命令 | 结果 |
|---|---|---|
| Go 全量测试 | `go test ./...` | 通过；helper 4.428s、mihomo 36.094s、network 2.907s；墙钟 39.2s |
| 前端测试 | `node --test tests/*.test.js` | 32/32 通过，150ms |
| 静态检查 | `go vet ./...` | 通过 |
| 格式检查 | `gofmt -l cmd internal` | **28 个文件**（详见 §1.2） |
| 工作区 | `git status --short` | 仅 `?? docs/design/` |
| 版本 | `web/index.html` / `tauri.conf.json` / `Cargo.toml` | v0.5.6-beta.3 / 0.5.6 / 0.5.6 |
| 线上清单 | `curl releases/latest/download/version.json` | 0.5.4，首字节 `{`，无 BOM，`notes` 内含 CRLF |

### 1.2 v1 未发现：本地格式门禁已失效

```
$ git ls-files --eol internal/mihomo/shutdown.go
i/lf  w/crlf  attr/text=auto eol=lf
$ git config core.autocrlf
true
$ gofmt -l cmd internal | wc -l
28        # 共 96 个 .go 文件
```

`.gitattributes` 在本轮基线（`0fe5149`）才加入 `eol=lf`，工作区仍是历史 CRLF，且全局 `core.autocrlf=true` 使已检出文件不会自动重写。结果：**CI 绿、本机红**，而 v1 附录把 `gofmt -l cmd internal` 列为复核命令，等于给了一条跑不过的命令。修正见 E4（不靠"重新检出全仓"解决，而是让检查脚本对 CRLF 免疫）。

### 1.3 复核方法

代码审读（Go/Rust/JS 全链路）+ 单测实跑 + 静态推导（超时预算、串行度、锁序）+ 线上资产实拉。**未做**：FCP/DCL 复测、f6-core 仓库审读、故障注入（镜像挂起）。凡未验证的结论在文中显式标注。

### 1.4 基线实测（批次 1 改动前，2026-11-28）

> **重要前提**：批次 1 的代码**尚未实施**（工作区在 `0bc4cc0`，`git status` 仅有 `docs/design/`）。因此本轮跑的是**改动前基线**，用途是：① 给 §5.1 的"现状"列一组真实数字（此前多为代码推导）；② 作为批次 1 验收的对照基准；③ 顺手验证 v2 对现状的判断是否成立。

完整的环境、原始输出、未覆盖项与复现步骤见 **`docs/design/2026-11-28-baseline-test-record.md`**（该文件在批次 1 实施后冻结不改，对照结果另记）。

测试对象：从 `0bc4cc0` 现场构建的 `freev6-helper.exe`，`FREEV6_ROOT` 指向临时空目录，端口 38111，token 为测试值。**未触碰本机正在运行的真实实例**（`freev6-desktop` + 其 helper）。

| 指标 | 方法 | 实测 |
|---|---|---|
| helper 冷启动 → `/status` 首次 200 | Go 小程序 spawn + 5ms 轮询，n=8 | **min 31 / p50 34 / mean 34 / max 37 ms** |
| `/status` | curl ×10，串行 | 1.4–3.3 ms，p50 ≈2.1 ms |
| `/runtime` | curl | 2.1 ms |
| `/notice`（12 候选并发） | curl | 274 ms |
| `/runtime/check`（12 候选、20s 上限） | curl | 1.11 s |
| `/update/latest`（stable，6 候选串行） | curl ×2 | 1.02 s / 0.48 s |
| `/update/latest?channel=beta`（两通道串行） | curl ×2 | 1.13 s / 0.96 s |
| 鉴权与来源守卫 | 7 组负例 | 无 token 401、错 token 401、`Origin: https://evil.example` 403、`Origin: null` 403、`Host: evil.example` 403、OPTIONS 204、GET `/proxy/start` 405 |
| 缺 SHA 的下载请求 | `PUT /update/download` + 轮询 progress | **PUT 先返回 200**，≤1s 后 progress=`error`「更新源校验失败：sha256 无效」 |
| 错误 SHA 的下载请求 | 同上 | progress=`error`「更新地址与官方清单不匹配」 |

**这组数字改变了什么：**

1. **健康网络下更新检查 ≈1s，不是 48s。** 48s/96s 是"每条线路都挂到 8s 超时"的上界（代码推导，本轮未做故障注入）。所以 A3 的收益必须分成两栏写：正常路径无变化（1s），退化路径 48/96s → ≤15s。§5.1 已改。
2. **`/runtime/check` 正常 1.1s，`/notice` 0.27s。** 说明 15s 客户端超时**正常时不会误伤**，真正的风险只在退化时——但那时服务端预算恰好也是 12–20s，**客户端超时值与服务端预算相等即必然先超时**（客户端计时还含建连与调度抖动）。§2.2.2 的论证据此收紧。
3. **冷启动总耗时只有 34ms 量级。** 所以"启动 +<10ms"是相对 34ms 的 **+30%**，不是相对"一秒"的零头——这个预算必须用同一把尺子量（§5.3），否则"加了 Job 也不到 10ms"这类说法没有对照。
4. **`/update/download` 对入参错误仍返回 200。** 前端因此先看到进度条、≤1s 后才看到红色错误。这是 A5 的具体表现，修正见 §3.1 A5（改为同步 400）。
5. 每次 `/update/latest` 都重新拉（0.48–1.13s 波动、无缓存复用），实测印证 A4：健康网络下多花 ~0.5–1s 与 6 个额外请求，退化网络下才会走到 96s。

**本轮未覆盖**（需要管理员 + TUN，不能在生产机上随意做）：`/proxy/start`、`/proxy/stop`、热点共享、退出/强杀收尾、Job 行为。这些留给批次 2 的真机矩阵。

---

## 2. 对 v1 的逐项判定

### 2.1 判定为正确、直接继承的部分

- **问题清单 17 项**：全部成立，行号可核，不再重复论证。
- **Job 由 helper 创建并持有**（而不是桌面壳创建、helper 加入）：正确，且能兼容现场出现过的混合构建。
- **验收边界改写**（硬杀只保证"无提权进程残留"，网络状态下次启动自愈）：正确，这是操作系统边界。
- **`MarkVerified(version)` 版本精确匹配**：正确，`store.Active()`（`main.go:838`）与 `MarkVerified()`（`main.go:901`）之间确实存在被 `ApplyUpdate`（`main.go:409`，不持 `h.mu`）换指针的窗口。
- **不引入设置修订号**：正确。settings 只有 helper 一个写者，修订号解决的是不存在的问题。
- **`.sig` 为附加资产、旧客户端透明**：正确，旧客户端只 GET `version.json`。
- **前端不引打包器/jsdom**：正确，现有 `web/lib/*.js` 已是 UMD 双模（`require` 可测），路径可持续。

### 2.2 必须修正的 7 处

#### 2.2.1 更新检查的并发只解决了一半：缓存不回填

v1 只写了"候选并发 + 15s 预算"和"`verifyUpdateSource` 复用已验证清单"，但**没有要求 `updateLatest` 写入缓存**。现状是：

- `updateLatest`（`cmd/freev6-helper/update.go:502`）每次都重新 `fetchLatestManifest`，从不写 `updateManifestCache`；
- `verifyUpdateSource`（`update.go:471`）只有缓存 Miss 时才拉，而缓存永远是冷的（除非用户在同一分钟内点两次更新）；
- 于是"检查更新 → 用户点立即更新"这条最常见的路径，会在**下载开始前**再做一次全量清单拉取。beta 客户端冷缓存时 = 两通道串行 × 6 候选 × 8s ≈ **96s**，进度条停在 0 不动。
- 补充实测（§1.4）：健康网络下这次重复拉取只值 0.5–1.1s，所以它在正常路径上是"多花 1s + 6 个额外请求"，只有在**退化网络**才暴露成最多 96s 的黑屏等待。两种情形都要修，但收益要分开写。
- 另一个同源问题：`PUT /update/download` 对"缺 SHA / SHA 与清单不符"仍**先返回 200**，错误只能靠轮询 progress 发现（实测 1s 后才变红）。入参可判的错误应当同步返回 400。

**修正**：`updateLatest` 把**验签通过**的 stable/beta 写进缓存（缓存时间戳即验签时间）；`verifyUpdateSource` 优先吃缓存，冷缓存预算 10s；`PUT /update/download` 在入参校验（SHA 缺失/格式非法/URL 不在白名单）失败时**同步 400**，只有"清单暂时拉不到"这类需要网络的判断才留在后台。验收指标改为"点更新后清单请求数 = 0"。

#### 2.2.2 D1 的"默认 15s"与 A 的服务端预算冲突

前端 `api()`（`web/app.js:184`）目前无超时。v1 的 D1 给"默认 15s，start/stop/download 180s"。但服务端真实预算是：

| 接口 | 服务端最坏预算 | 实测（健康网络，§1.4） | 15s 默认超时的后果 |
|---|---|---|---|
| `/update/latest` | A3 之后 15s（现在是 48/96s） | 1.0s | 与服务端预算**相等** → 客户端必然先超时 → 前端判"无更新" |
| `/runtime/check` | 20s（`coreupdate.go:151` 总 ctx） | 1.1s | 必然先超时 → 内核更新漏报 |
| `/notice` | 12s（`notice.go:296` 总预算） | 0.27s | 公告偶发丢失 |

关键是"相等即错"：客户端超时值等于服务端预算时，网络抖动、建连、调度都会让客户端先放弃，用户看到的是"没有更新"而不是"检查失败"——**这是最坏的一类错误**（静默错误结论）。所以超时不能拍脑袋定，必须由服务端预算推导：

| 接口组 | 服务端预算 | 前端超时（≥ 预算 ×1.5，最少 +5s） |
|---|---|---|
| `/proxy/start`、`/proxy/stop` | ICS 最坏 120s | 180s |
| `/update/latest` | 15s | 25s |
| `/runtime/check` | 20s | 30s |
| `/notice` | 12s | 20s |
| `/update/download`、`/runtime/download`（PUT/POST，同步校验后即返回） | <1s | 10s |
| `/status`、`/settings`、`/progress`、其余 | <1s | 10s |

**修正**：按上表实现分层超时（批次 3 的 D1），并把"新增接口必须同时登记服务端预算与前端档位，且超时 ≥ 预算×1.5"写进评审规则（§5.4）。另需注意：`api()` 现有的"网络错误回落 13335 端口重试一次"必须只对**连接失败**生效，超时（`AbortError`）不得触发第二次重试，否则最坏等待翻倍。

#### 2.2.3 孤儿接管"先等 8s"加在启动关键路径上

`listenOrTakeOver`（`cmd/freev6-helper/port.go:20`）在端口占用时今天的动作是"立刻 kill + 轮询 3s"。v1 的 B7 改为"先等 8s 再 kill"，最坏让**冷启动多 8s**——而冷启动正是用户最敏感、也是最不该退化的路径。且该场景已被壳侧 `kill_stray_helpers()`（`main.rs:738`）覆盖大半。

**修正**：等待上限 3s，且只在"占用者确实是 helper 且父进程已消失"时等待（现判定已具备）；超时后照旧 kill + 3s 轮询。总增量 ≤3s，仅孤儿场景。

#### 2.2.4 更新门禁 15s + 15s

v1 B6：shutdown 预算 15s → 等 helper 退出 15s → 未退出则中止。正常路径里前端 `startUpdate()` 已先调 `stopProxyApi()`，所以壳侧这次 shutdown 是**无事可做的第二道保险**；而无事可做时的等待应该是几十毫秒。

**修正**：shutdown 6s、等退出 6s，合计 ≤12s；超时中止并给出可操作文案（"请手动退出 FreeV6 后重新更新"）。**不允许**把 120s 的 ICS 还原放进壳侧等待窗口。

#### 2.2.5 `RunEvent::Exit` 里阻塞等待

`RunEvent::Exit`（`main.rs:844`）今天只 `child.kill()`，瞬时。v1 改成"shutdown（15s）后 kill 兜底"——helper 挂死时用户会看到**窗口关不掉**。

**修正**：Exit 阶段只做"发一个带 1.5s 超时的 shutdown 请求"，不做长等待；真正的收尾由 helper 自己在后台完成（客户端断开不影响它，见 2.2.6），壳侧 3s 后无条件 kill 兜底。

#### 2.2.6 shutdown 端点缺三处边界

1. **`http.Server.Shutdown` 无超时**：`WriteTimeout: 0`（`main.go:204`）且 `startProxy` 可能跑十几秒，`Shutdown` 会一直等 handler 结束 → "优雅退出"变"永不退出"。必须 `Shutdown(ctx 5s)` + 超时 `Close()`。
2. **客户端断开是否取消收尾**：壳侧只等 1.5s 就放弃连接，如果 handler 用 `request.Context()`，收尾会被当场取消 → 热点还原半途而废。**必须**用 `context.WithoutCancel` + 固定预算（`stopProxy` 现在已这么写，shutdown 端点要沿用）。
3. **与 `h.mu` 的关系**：`startProxy` 持 `h.mu` 可达数十秒，shutdown 需要拿同一把锁；必须"有界等锁 + 超时返回 409/503"，不能死等。

#### 2.2.7 会话锁有界等待必须看返回码

`startlock_windows.go` 现在：

```go
if _, err := windows.WaitForSingleObject(handle, windows.INFINITE); err != nil { ... }
```

返回码被丢弃。改成有界等待后，`WAIT_TIMEOUT`（258）**不是 error**（`x/sys` 只在 `WAIT_FAILED` 时返回 err），代码会以为拿到了锁 → 两个进程同时进入临界区，恰好制造它要防的竞态。

**修正**：显式判定 `event == windows.WAIT_OBJECT_0 || event == windows.WAIT_ABANDONED`（后者是持有者被杀，语义上算获取成功），`WAIT_TIMEOUT` 返回可读错误。已确认 `x/sys v0.47.0` 导出 `WAIT_OBJECT_0`/`WAIT_ABANDONED`，`WAIT_TIMEOUT` 为 `syscall.Errno(258)`。

### 2.3 v1 漏掉的 6 处

#### 2.3.1 壳侧启动即杀，会打断上一实例的收尾

`kill_stray_helpers()`（`main.rs:738`）在 `start_helper` 之前无条件 `taskkill /F /IM freev6-helper.exe`。单实例互斥成立时"还在跑的 helper 都是孤儿"这个前提没错，但**孤儿可能正在热点还原的 1–5s 里**。B2 上线后这刀还会连带杀掉它的核心（Job 关闭）。

**修正**：与 B7 同一策略——启动时先等 ≤3s 让镜像名消失（helper 的父进程监视会主动退），再 kill。抽成 `reap_stray_helpers(budget)`，`update_apply` 与启动共用。

#### 2.3.2 备用配置被公告缓存锁死

`cloudProfileUnlocked`（`cloudconfig.go:54`）→ `loadNoticeCache` 超过 `noticeCacheMaxAge`（7 天，`notice.go:218`）即返回 `false` → `startProxy` 直接 400 `cloud-locked`（`main.go:658`）。也就是说：**用户导入过备用配置，离线（或镜像全挂）超过 7 天，就无法启动免流**。

**修正**：把"门禁判定"与"展示新鲜度"拆开——门禁只看**最后一次成功拉取的结论**（不因过期翻转为关闭）；7 天只用于 UI 提示"公告已过期"。风险：维护者关闭开关后，长期离线客户端会延迟生效（可接受：这是资源保护开关，不是急停开关）。此项需拍板（§8 D8）。

#### 2.3.3 9090 半死会让节点列表永久冻结

`fetchProxies`（`app.js:2638`）无超时，且 `state.proxiesPolling` 单飞：一次请求挂住 → 标志永远为 `true` → 后续每 5s 的轮询全部早退，节点列表定格到下次刷新。`switchProxy`（`app.js:2766`）也不检查 `resp.ok`，切失败仍改本地 `now`。

**修正**：3s `AbortSignal.timeout` + `finally` 复位标志（已有 `finally`，缺的是超时）+ `resp.ok` 检查 + 失败回滚。

#### 2.3.4 `update_apply` 改优雅关停后必须先置 `stopping`

`HELPER_TERMINATED` 的 `Terminated` 分支（`main.rs:926-945`）在 `!stopping` 时弹"组件异常"并 `app.exit(1)`。`stop_helper`（`main.rs:982`）之所以安全，是因为它先 `stopping.store(true)` 再 kill。改成"先 shutdown 再等退出"时若忘了置位，**正常更新会被自己判成崩溃**。

**修正**：拆出 `mark_helper_stopping()`，`update_apply`/`Exit` 在发 shutdown 前调用；`stop_helper` 复用它。

#### 2.3.5 前端拆分对象是经典脚本，不是模块

`index.html:807-813` 依次加载 `traffic.js`、`ipv6gate.js`、`lib/*.js`、`app.js`、`halloween.js`，全是经典脚本。经典脚本共享**同一个全局词法环境**：两个文件各自 `const $ = ...` 时，第二个文件整体 `SyntaxError` 不执行——页面不白屏，只是那部分功能静默消失，比白屏更难查。

**修正**：拆分必须沿用 `web/lib/version.js` 的 UMD 包裹（IIFE + `module.exports`/`root.FreeV6Xxx` 双出口），不允许裸搬移；`web/*.js` 与 `web/lib/*.js` 已由 CI 做 `node --check`，但语法检查**发现不了跨文件重名**，需在评审清单里加一条。

#### 2.3.6 本地格式门禁失效

见 §1.2。修正：新增 `scripts/check-format.py`（或 `.ps1`），把工作区文件读入后把 CRLF 归一化为 LF 再 `gofmt -d`，本机与 CI 共用；`.gitattributes` 追加 `*.sig binary` 与对 `version.json` 的 `-text` 保护（防止未来把清单提交进仓库后行尾被改写、破坏签名）。

### 2.4 判定为"不做"的

与 v1 一致：不引入设置修订号、不为仓库结构造检查脚本、不把文档站并入本仓库、不引入 jsdom/Playwright CI、不做安装包 Authenticode 签名（另案）。

新增两条"不做"：

- **不做"下载后再校验安装包哈希"**：`update/download` 已验 SHA 并落到 `%LOCALAPPDATA%`，本地替换需要同用户代码执行能力，收益低于成本（无 SHA256 依赖可加）。
- **不做 Linux/macOS 的 Job 等价物**：`job_other.go` 留空实现即可，不为未发布的平台承担复杂度。

---

## 3. 改进清单（重新梳理）

优先级：P0 = 安全或"必然导致故障/数据不一致"；P1 = 真实缺陷，有可复现触发路径；P2 = 工程债，影响迭代速度；P3 = 顺手。

### 3.1 A 组 · 更新与供应链

| ID | 改进项 | P | 核心理由 | 预期收益 | 主要风险 | 验证指标 |
|---|---|---|---|---|---|---|
| A1 | 应用清单 Ed25519 签名（`v1.<keyId>.<b64>`） | P0 | 清单与安装包同源同镜像，可被同时投毒 | 投毒需私钥；通道可验证 | 丢钥/换钥；首版过渡 | 篡改、缺签名、未知 key 100% 拒绝 |
| A2 | 内核清单签名（f6-core，跨仓库） | P0 | 内核以提权身份执行，攻击面同级 | 同上 | 跨仓库协调 | 替换 `core.json`+内核包被拒 |
| A3 | 更新检查有界并发 + 15s 总预算 | P0 | 6 候选 × 8s 串行 = 48s，beta 双通道 96s | 最坏 96s → ≤15s | 6 路并发对镜像的压力 | 挂起线路下 ≤ 预算返回 |
| A4 | `updateLatest` 回填已验证缓存 | P1 | 检查与下载之间重复拉两路（最坏 96s 才开始下载） | 点更新后 0 次清单请求 | 缓存被污染 | 只存验签通过者；计数断言 |
| A5 | 缺 SHA 时禁止发起下载；`PUT /update/download` 入参错误同步 400 | P2 | UI 称"跳过校验"，helper 实测先回 200、≤1s 后才报错（§1.4） | 错误更早更明确 | 旧前端会收到 400（其 `api()` 已按 `payload.error` 抛错，兼容） | 无 SHA 不发 PUT；服务端同步 400 |
| A6 | 密钥生成/备份/轮换文档 + Secret 缺失即失败 | P1 | 丢钥 = 通道永久失效 | 可恢复 | 文档滞后 | 未配 Secret 时发布失败 |

### 3.2 B 组 · 进程与生命周期

| ID | 改进项 | P | 核心理由 | 预期收益 | 主要风险 | 验证指标 |
|---|---|---|---|---|---|---|
| B1 | 共用 `StopSession`（GUI/CLI/退出） | P0 | GUI 与 CLI 停止顺序不同（缺关 TUN） | 一套语义、一处修复 | 重构停止路径 | 顺序断言测试 |
| B2 | helper 自有 Job（`KILL_ON_JOB_CLOSE`） | P0 | 杀 helper 留下无主提权核心 | 无残留核心 | 嵌套 Job 失败 | 杀 helper 后核心消失 |
| B3 | shutdown 端点 + 父进程监视 | P0 | 壳被强杀后无人收尾 | 硬杀也能优雅收尾 | 旧壳无 env；PID 复用 | 杀壳后数秒内收尾 |
| B4 | 壳侧退出/更新门禁（`helper_client.rs`） | P0 | 更新产生混合构建 | 不再混合构建 | 增加退出等待 | 更新前必须确认退出 |
| B5 | 会话锁覆盖 Stop 临界区 | P1 | 并发启停会删掉新会话的路由 | 消除竞态 | 有界等待 | 并发 start/stop 测试 |
| B6 | `MarkVerified(version)` 版本精确 | P1 | 会把没跑过的内核标成已验证 | 回滚位可信 | 无 | 交错启动/更新测试 |
| B7 | 孤儿接管先等后杀（≤3s） | P1 | 杀掉正在收尾的旧 helper | 不中断还原 | 启动多等 | 孤儿退出中不 kill |
| B8 | 壳侧 `reap_stray_helpers` 先等后杀 | P1 | 启动即杀，打断上一实例收尾 | 同上 | 启动多等 | 同 B7 |
| B9 | CLI 停止对齐（补关 TUN） | P1 | CLI 停完网络恢复更慢 | 体验一致 | 需读 controller secret | CLI stop 后路由立即回收 |

### 3.3 C 组 · 配置一致性

| ID | 改进项 | P | 核心理由 | 预期收益 | 主要风险 | 验证指标 |
|---|---|---|---|---|---|---|
| C1 | 内容文件 `writeFileAtomic` + `configMu` | P1 | 原地截断写；内容与开关两步提交 | 读者只见新旧一致版本 | 锁顺序 | 并发导入+启动测试 |
| C2 | reset 顺序反转（先 settings 后删文件） | P1 | 失败留下"文件没了、开关还开着" | 失败仍可用 | 无 | 注入写失败测试 |
| C3 | 明确锁序：`configMu` → `settingsMu`，禁止反向 | P1 | 新锁引入死锁风险 | 可推理性 | 设计约束 | `-race` + 评审 |

### 3.4 D 组 · 前端

| ID | 改进项 | P | 核心理由 | 预期收益 | 主要风险 | 验证指标 |
|---|---|---|---|---|---|---|
| D1 | `api()` 分层超时（按服务端预算推导） | P1 | 无超时；一刀切 15s 会砍长预算接口 | 卡死可恢复且不误伤 | 档位设错 | 弱网 15s 内报错；长接口不超时 |
| D2 | 9090 直连超时 + `resp.ok` + 失败回滚 | P1 | 卡死导致节点列表永久冻结 | 可自愈 | 无 | 注入挂起 fetch |
| D3 | 拆分为 UMD 模块（IIFE 包裹） | P2 | 4011 行；顶层 `const` 冲突 | 可测试可维护 | 加载顺序/全局名 | 首屏不劣化；`node --test` 覆盖 |
| D4 | 纯逻辑测试（候选选择/超时/轮询门控） | P2 | 现仅覆盖纯函数 | 回归保护 | 无 | `node --test` 通过 |

### 3.5 E 组 · 门禁与工程

| ID | 改进项 | P | 核心理由 | 预期收益 | 主要风险 | 验证指标 |
|---|---|---|---|---|---|---|
| E1 | CI `permissions`/`timeout`/`concurrency` + Actions 固定 SHA + dependabot + **Go 工具链对齐** | P2 | 供应链与资源占用；实测本机 go1.26.3、CI 1.25.x、`go.mod` 无 `toolchain` 行 → "本机能跑 CI 跑不过" | 降低风险 | 无 | workflow 校验；两处 `go version` 一致 |
| E2 | 版本守卫抽成脚本（CI/release 共用） | P3 | 两处各写一份 | 一处修复 | 无 | 改一处两边生效 |
| E3 | `AGENTS.md`/`SECURITY.md`/`CONTRIBUTING.md` | P2 | 新会话无上下文，反复返工 | 降低沟通成本 | 文档腐化 | 评审 |
| E4 | CRLF 免疫的格式门禁 | P2 | 实测 28/96 文件本机永久脏 | 本地=CI | 无 | 本机 exit 0 |
| E5 | 镜像前缀单一来源（`internal/ghmirror`） | P3 | `update.go` 与 `coreupdate.go` 各一份 | 一处改全局生效 | 触及内核更新 | 单测 |
| E6 | 慢测试分组（`-short`） | P3 | `internal/mihomo` 36s | 本地循环更快 | 覆盖流失 | `-short` 下 <5s |

### 3.6 F 组 · 可用性

| ID | 改进项 | P | 核心理由 | 预期收益 | 主要风险 | 验证指标 |
|---|---|---|---|---|---|---|
| F1 | 备用配置门禁不因公告过期锁死 | P1 | 离线 7 天 → 已导入配置无法启动 | 可用性 | 维护者开关延迟生效 | 断网 8 天后仍可启动 |

---

## 4. 兼容性

### 4.1 版本矩阵（批次 1 + 2 落地后）

| 桌面壳 | helper | 客户端 | 行为 |
|---|---|---|---|
| 新 | 新 | 新 | 全量能力：签名强校验、shutdown、父进程监视、Job |
| 新 | 旧 | 新 | shutdown 404 → 回落 `/proxy/stop` + 杀进程；更新门禁更严格（可能中止） |
| 旧 | 新 | 新 | 旧壳直接杀；父监视不启用（无 `FREEV6_PARENT_PID`）；Job 仍保护核心 |
| 旧 | 旧 | 新 | 与今天一致（回退基线）；签名强校验仍生效 |
| 任意 | 任意 | 旧（≤0.5.4） | 只读 `version.json`，`.sig` 完全透明 |

### 4.2 数据与格式兼容

| 资产 | 约束 | 违反后果 |
|---|---|---|
| `state/mihomo.pid` | 单行十进制，Rust 按 `u32` 直读 | 内存卡片显示错误/解析失败 |
| `state/mihomo.pid.meta` | `{pid,startTime}` sidecar，不得合并进 pid 文件 | 同上 |
| `config/settings.json` | 只新增字段；"省略即保留"语义不变 | 一次普通保存把链式/备用配置关掉 |
| `version.json` | **签名字节 = 上传字节**；不得被任何工具做行尾/编码归一 | 全量客户端拒绝更新 |
| `version.json.sig` | 必须二进制安全传输；`.gitattributes` 加 `*.sig binary` | 同上 |
| `state/network-snapshot.json` | 结构只加字段 | 热点还原失败 |
| `state/cores/index.json` | 只加字段；`Active/Previous` 语义不变 | 回滚位错乱 |

### 4.3 唯一一处有意的不兼容

**helper 被终止时，核心随 Job 一并退出**（今天会留下无主核心）。这是产品决策（§8 D1），需要在 CHANGELOG 明写；对"用户手动结束 helper 后想继续用核心"的用法是破坏性的——但该用法本身不受支持（UI 无法控制无主核心）。

### 4.4 发布一致性规则（v1 未明写）

> **客户端强校验与流水线签名必须在同一个 tag 内成对上线。**

任何一半单独发布都会造成事故：先上客户端 → 所有更新被拒；先上流水线 → 无影响但白等。执行方式：批次 1 的全部改动（helper + 前端 + 两个 workflow + `docs/release.md`）在同一个 PR 合入，打一个 beta tag 验证，再发正式版。

### 4.5 稳定性保证边界（改写 v1 §2.2 的验收）

| 场景 | 能保证 | 不能保证 |
|---|---|---|
| 正常退出（无免流） | 无核心/helper 残留 | — |
| 正常退出（有免流，先停止） | 网络状态还原、快照删除 | — |
| 任务管理器结束桌面壳 | helper 数秒内完成 StopSession；无残留进程；热点**通常**可还原 | helper 同时挂死时，热点留待下次启动自愈 |
| 结束 helper | 核心与临时子进程随 Job 退出 | 网络状态（热点）留待自愈 |
| helper 挂死 + 正常退出壳 | 壳 ≤3s 后强杀 → Job 关闭 → 核心退出 | 同上 |
| 更新安装 | helper 确认退出后才启动 NSIS | 安装器自身失败（另案） |

---

## 5. 性能与耗时预算

### 5.1 热路径预算

| 路径 | 现状（实测见 §1.4） | v2 上限 | 变化 | 守护方式 |
|---|---|---|---|---|
| helper 冷启动 → `/status` 200 | **31–37ms（p50 34ms）** | **+<10ms** | 相对 34ms 的 +30% 以内 | `scripts/boot-measure`（§5.3） |
| 壳 → 可交互（含 webview 首帧） | 未测（本轮范围外） | 不劣化 | ≈0 | Chrome Performance |
| `/status`（3s 轮询） | **p50 2.1ms** | 不变 | 0 | 不加 IO/子进程 |
| helper 绑定端口（孤儿场景） | kill + 3s 轮询 | ≤3s 等待 + 3s 轮询 | 0 | B7 单测 |
| `/proxy/start` | 快照/渲染/就绪/ICS，ICS 最坏 120s | 不变 | 0 | 不新增锁外等待 |
| `/proxy/stop`（无热点） | 0.3–1.2s（代码推导；本轮未测，需管理员 + TUN） | 不变 | 0 | `helper.log` stop 行 |
| `/proxy/stop`（有热点） | 1–5s | 不变 | 0 | 同上 |
| **退出（无免流）** | ≈0（kill） | **≤100ms** | +≤100ms | 壳侧计时 |
| **退出（有免流，已先停止）** | ≈0 | **≤100ms** | +≤100ms | 同上 |
| **退出（helper 挂死）** | ≈0 | **≤3s** | +≤3s | 门禁单测 |
| `/notice` | **0.27s** | 不变（12s 上限） | 0 | 预算测试 |
| **更新检查（stable）** | **正常 1.0s**；最坏 48s（推导） | **正常 ~1s；最坏 ≤15s** | 退化路径 −33s | 挂起镜像测试 |
| **更新检查（beta）** | **正常 1.0–1.1s**；最坏 96s（推导） | **正常 ~1s；最坏 ≤15s** | 退化路径 −81s | 同上 |
| **点更新后的源校验** | 正常 0.5–1.1s；冷缓存最坏 96s | **0（命中）/≤10s（冷）** | 正常 −1s，最坏 −86s | 缓存计数断言 |
| `/runtime/check` | **1.1s**；上限 20s | ≤20s（签名请求必须并发） | 0 | 同预算测试 |
| 验签 | — | ≤1ms（Ed25519 64B ≈ 50–100µs） | 可忽略 | benchmark |
| 前端轮询稳态 | 3s/1s/5s/3s | 不变 | 0 | DevTools Network |
| 前端首屏 | FCP 176–208ms / DCL 393–448ms（2026-11-27 历史基线，本轮未复测） | 不劣化 >10% | 0 | Chrome Performance |

### 5.2 v1 会引入的 5 处回退与替换值

| # | v1 的写法 | 问题 | v2 的替换 |
|---|---|---|---|
| 1 | 孤儿接管先等 8s | 冷启动 +8s | 3s，且仅孤儿场景 |
| 2 | 更新门禁 15s + 15s | 点更新后最多干等 30s | 6s + 6s |
| 3 | `RunEvent::Exit` shutdown 15s | 窗口"关不掉" | 1.5s 发请求 + 3s 无条件 kill |
| 4 | `api()` 默认 15s | 砍掉 15s/20s 预算的接口 | 分层档位（§6 表） |
| 5 | 每候选"清单 + 签名"若串行 | 15s → 30s；内核 20s → 40s | 候选间并发 **且** 清单/签名并发 |

### 5.3 测量方法

**不要用"启动壳 + 掐表"的方式测启动**：shell 自身的启动开销（数百毫秒到 1s）会完全淹没 helper 的 34ms。本轮用的是"进程内 spawn + 5ms 轮询 `/status`"的 Go 小工具（约 80 行，仅标准库）：

```
spawn(freev6-helper.exe, FREEV6_ROOT=临时空目录, FREEV6_API_TOKEN=测试值, FREEV6_API_PORT=高位端口)
loop: GET /api/v1/status（带 Bearer）→ 首个 200 记时 → kill 子进程
n=8，输出 min/p50/mean/max
```

实测输出（§1.4）：`min=31ms p50=34ms mean=34ms max=37ms`。判据：批次 1 落地后 p50 相对 34ms 的增量 **≤10ms**；批次 2（Job + 父进程监视）同样以 34ms 为基准。

该工具建议作为 `scripts/boot-measure/main.go` 进仓库（批次 1 交付项），否则下次又要临时写一遍、数字不可比。

其余指标的测量方式：`/proxy/stop` 用 `helper.log` 的 `helper: stop: ... total` 行；退出延用壳侧日志时间戳；更新检查用 Go 单测里的 `httptest` 挂起服务器（不依赖真实网络）；前端用 DevTools Network 的 Time 列。

### 5.4 规则（写进 `AGENTS.md`）

> 任何新增网络调用都必须在同一 PR 里给出：① 单次超时；② 总预算；③ 失败时的降级路径；④ 一个断言预算的测试。没有预算的串行等待不允许合入热路径（启动、停止、退出、更新检查）。
>
> 前端超时一律由服务端预算推导：**超时 ≥ 预算 × 1.5（最少 +5s）**，与服务端预算相等视为配置错误（§2.2.2 表）。

---

## 6. 首批实施（批次 1 · 更新信任链与时延）

### 6.1 范围

**做**：A1–A6。**不做**：生命周期（批次 2）、配置（批次 3）、前端拆分（批次 4）、内核签名的跨仓库落地（A2 的客户端侧代码本批完成，`f6-core` 侧发布流程单独排期，但接口与测试先行）。

### 6.2 逐文件改动

| 文件 | 动作 | 要点 | 验证 |
|---|---|---|---|
| `internal/appupdate/keys.go` | 新增 | `//go:embed update_public.pem`；`map[keyID]ed25519.PublicKey`；`PublicKey(id)` | 单测：未知 key 返回 false |
| `internal/appupdate/verify.go` | 新增 | `Verify(manifest, sig []byte) error`；解析 `v1.<keyId>.<b64>`；长度/编码/未知 key 全部拒绝；`verifyWith(keys, ...)` 供测试注入 | 单测：有效/篡改一字节/截断/空/未知 key |
| `internal/appupdate/sign.go` | 新增 | `Sign(manifest []byte, key ed25519.PrivateKey, keyID string) ([]byte, error)`，与 `Verify` 严格对称 | 往返测试 |
| `internal/appupdate/update_public.pem` | 新增 | 仅公钥；私钥只进 GitHub Secret 与离线备份 | 发布文档 |
| `cmd/freev6-update-sign/main.go` | 新增 | `-in/-out/-key-id/-key-file`；`-verify`；原子写；私钥缺失非零退出 | 单测 + 手工 |
| `cmd/freev6-helper/update.go` | 修改 | ① `fetchVerifiedManifest`：候选并发、每候选成对取 `version.json` + `.sig`、总预算 15s、首成功即返回；② `updateLatest`：beta 客户端两通道**并发**，并把验签通过者写入缓存；③ `verifyUpdateSource`：先吃缓存、冷缓存 10s、不再串行重拉两路；④ 缓存只写入验签通过的清单（缓存时间戳即验签时间），未验证内容不得写入；⑤ `PUT /update/download` 对 SHA 缺失/格式非法、URL 不在白名单**同步 400**（今天先 200 再后台报错，见 §1.4） | 见 6.3 |
| `scripts/boot-measure/main.go` | 新增 | 标准库小程序：spawn helper → 轮询 `/status` → 输出 min/p50/mean/max。给 §5.3 一个可复现的启动基线 | 本机 n=8 跑通 |
| `cmd/freev6-helper/update_test.go` | 修改 | 新增 7 个用例（见 6.3） | `go test` |
| `web/app.js` | 修改 | 删"未附 SHA256，跳过校验"分支：`!info.sha` 直接 `updateFailed('更新清单缺少校验值，已停止下载')`，不发 PUT | 手工 + 评审 |
| `.github/workflows/release.yml` | 修改 | 生成 `version.json` 后签名；Secret 缺失即失败；先传 `.sig` 再传 `version.json`；attestation 含 `.sig`；发布后**下载回来验签** | 干跑 |
| `.github/workflows/beta-channel.yml` | 修改 | 同时下载/上传 `.sig`，顺序同上，上传后验签；任一步失败即 job 失败 | `workflow_dispatch` |
| `docs/release.md` | 修改 | 密钥生成/备份/轮换；Secret 配置；同步失败修复；首版过渡说明 | 评审 |
| `CHANGELOG.md` | 修改 | 安全与性能两条 | 评审 |
| `.gitattributes` | 修改 | `*.sig binary`、`version.json -text` | 评审 |

### 6.3 新增测试（必须全绿）

`internal/appupdate/verify_test.go`
1. 有效签名通过；2. 篡改 1 字节拒绝；3. 未知 keyId 拒绝；4. 截断/空/非法 base64 拒绝；5. 签名长度非 64 字节拒绝；6. Sign→Verify 往返。

`cmd/freev6-helper/update_test.go`（沿用现有 `httptest` + 变量注入风格）
1. `TestUpdateLatestBudgetWithHungMirror`：6 条候选中 1 条挂起 30s，压缩预算后断言总耗时 ≤ 预算；
2. `TestUpdateLatestFetchesChannelsConcurrently`：stable 与 beta 各挂 400ms，断言总耗时 < 700ms（串行会是 800ms+）；
3. `TestUpdateLatestPopulatesVerifiedCache`：调 `updateLatest` 后调 `verifyUpdateSource`，断言服务端请求计数不再增长；
4. `TestFetchVerifiedManifestRejectsPairMismatch`：清单来自 A、签名来自 B → 拒绝；
5. `TestFetchVerifiedManifestRejectsUnsigned`：无 `.sig` → 拒绝（"无更新"而非"更新到未签名版"）；
6. `TestVerifyUpdateSourceColdCacheBudget`：冷缓存两路各挂起，断言 ≤10s 返回错误；
7. `TestUpdateDownloadRejectsMissingSHA`：`PUT` 直接 400，且**不启动下载 goroutine**（progress 仍为 idle）。

前端：`tests/api-timeout.test.js`（若批次 1 顺带做 D1；否则留到批次 3）。

### 6.4 本地验证命令

```bash
go build ./... && go vet ./...
go test ./internal/appupdate ./cmd/freev6-helper -run 'Verify|Sign|Update' -v
go test ./...                 # 期望：全绿，墙钟 39s 量级不劣化
node --test tests/*.test.js   # 期望：32/32（或新增用例后 N/N）
go run ./scripts/boot-measure -bin ./freev6-helper.exe -root /tmp/fv6-root   # 期望 p50 ≤44ms（基线 34ms）
python scripts/check-format.py   # 期望：exit 0（E4 落地后）
```

### 6.5 实机测试方案（Windows，beta 周期）

前置：生成密钥对一次；私钥进 `FREEV6_UPDATE_SIGNING_KEY`，公钥进仓库；离线备份私钥。

| # | 用例 | 步骤 | 期望 | 观测点 |
|---|---|---|---|---|
| T0 | 改动前后对照 | 先复跑 §1.4 的五个指标，改动后再跑一次 | 启动 p50 ≤44ms；`/update/latest` 正常路径 ≤1.5s；`/runtime/check` 不劣化 | 两份输出并列贴到 PR |
| T1 | 本地单测 | §6.4 | 全绿，`update` 相关用例 ≤5s | 终端输出 |
| T2 | 流水线干跑 | `workflow_dispatch` 触发 beta-channel（不带新版本） | 下载→上传→回验全通过；故意删 Secret 时 job 失败 | Actions 日志 |
| T3 | 发布签名版 | 打 `v0.5.7-beta.1` | 草稿 release 含 `version.json` + `version.json.sig`；资产可回验 | release 页面 + `freev6-update-sign -verify` |
| T4 | 旧客户端升级 | 装 0.5.6-beta.3 → 检查更新 → 立即更新 | 升到 beta.1；`/update/latest` 耗时 ≤1.5s（正常网络） | DevTools Network 时长、关于页版本 |
| T5 | 投毒演练 | 在 beta-channel 上传伪造 `version.json`（**保留旧 `.sig`**） | 客户端提示"更新源校验失败"，不发起下载 | 前端错误文案 + 无 `freev6-update-setup.exe` 新文件 |
| T6 | 指针恢复 | 重新跑 beta-channel 同步 | 客户端恢复"发现新版本" | 同上 |
| T7 | 时延（黑洞镜像） | `hosts` 把 `v6.gh-proxy.org` 指向 `203.0.113.1`，检查更新 | 总耗时 ≤10s 且能成功（其余候选并发返回） | 秒表 + DevTools |
| T8 | 时延（全黑洞） | 把 5 个镜像 + github.com 全部黑洞 | ≤10s 返回"无更新"，UI 不卡死 | 同上 |
| T9 | 回滚 | 删除 beta tag / 把 beta-channel 指回上一版 | 客户端回到"已是最新"，不降级安装 | 关于页 |

补充：A2（内核签名）在 `f6-core` 侧完成后单独回归——替换 `core.json` + 内核包 → 拒绝安装；验签失败时回退到**上次已验证**的缓存清单。

### 6.6 回滚

- 签名是纯增量资产：`.sig` 留在 release 上无副作用。
- 代码回滚只能在"已带验签的客户端"之后执行，且回滚后的版本必须**仍然产出有效签名清单**（否则新客户端拒绝）。
- 密钥丢失的恢复流程必须在本批写进 `docs/release.md`（新 key id + 先发含新公钥的客户端，再换签名钥）。

### 6.7 交付清单

- [ ] `internal/appupdate`（4 文件）+ 单测全绿
- [ ] `cmd/freev6-update-sign` + `-verify` 可用
- [ ] `update.go` 并发/缓存/验签/入参校验四处改造 + 7 个用例
- [ ] 前端缺 SHA 分支删除
- [ ] 两个 workflow 的签名与上传顺序
- [ ] `docs/release.md` 密钥运维章节
- [ ] 一次 beta 发布 + T4–T9 记录（贴到 PR）

---

### 6.8 批次 1 实施偏差（落地后回填）

实施完成于 2026-11-28，逐条验证结果见 `2026-11-28-batch1-test-record.md`。相对 §6.2/§6.3 的差异：

| 项 | 计划 | 落地 |
|---|---|---|
| 缓存字段 | 新增 `verifiedAt` | 复用缓存时间戳 `at` 表示验签时间（只有验签通过才写入，两者等价；少一个冗余字段） |
| keyId | `-key-id` 必填 | 可选；省略时用公钥指纹，客户端对"显式 id"与"指纹 id"两套都认 |
| 签名工具 | `-in/-out/-key-id/-key-file/-verify` | 增加 `-gen-key`（生成密钥对：私钥只落文件、公钥打印到 stdout）与 `-pub-out` |
| `update_public.pem` | 内置生产公钥 | **暂为空占位**（只有说明注释）。缺公钥时验签返回 `未内置更新公钥`，发布流水线的"回验"步骤会因此拒绝出包——这是有意的门禁 |
| 测试数量 | 7 个（helper 侧） | helper 侧 9 个、appupdate 10 个、签名工具 3 个；`TestFetchVersionJSON` 并入 `TestFetchBody`（原函数已成死代码，删除） |
| 内核签名（A2） | 客户端侧本批完成 | 客户端侧已就绪（`appupdate` 通用），`f6-core` 仓库发布流程待排期 |

**阻塞项（需要人工操作，Agent 不执行）**：生成签名密钥对 → 把公钥追加进 `internal/appupdate/update_public.pem` 并提交 → 私钥内容填进 GitHub Secret `FREEV6_UPDATE_SIGNING_KEY` → 离线备份私钥。步骤见 `docs/release.md`「签名密钥运维」。

---

## 7. 后续批次与门禁

| 批次 | 内容 | 前置 | 估算 | 门禁 |
|---|---|---|---|---|
| 2 | B1–B9（生命周期/Job/shutdown/会话锁/MarkVerified/CLI） | 批次 1 合入 | 3–4 天 + 1 天真机 | §4.5 全表通过；无残留进程 |
| 3 | C1–C3 + D1/D2 + F1 | 可与 2 并行 | 1.5 天 | 并发导入/注入失败/弱网测试 |
| 4 | D3/D4（UMD 拆分 + 纯逻辑测试） | 批次 3 | 2–3 天 | 首屏不劣化；每步单独提交 |
| 5 | E1–E6 | 任意 | 0.5–1 天 | CI 绿；本机格式门禁绿 |

每批统一门禁：`go test ./...`、`node --test`、`go vet`、格式脚本、`git status` 干净、CHANGELOG 更新、英文提交信息、涉及发布流程的同步更新 `docs/release.md`。

---

## 8. 待拍板决策

| # | 决策 | 推荐 | 理由 |
|---|---|---|---|
| D1 | helper 被终止时核心是否随 Job 退出 | **是** | 无主提权进程无法被 UI 控制；退出语义本来就是"先停止" |
| D2 | 壳被强杀时 helper 是否自动收尾 | **是** | 用户文档承诺"退出尽量恢复网络"；父监视是唯一可行手段 |
| D3 | 更新安装遇到 helper 未确认退出 | **中止安装** | 混合构建比"更新失败"更难恢复 |
| D4 | 签名格式是否带 key id | **是** | 换钥/丢钥不会让通道永久失效 |
| D5 | 首批是否包含内核签名（跨仓库） | **客户端侧本批完成；发布侧单独排期** | 接口与测试先行，避免被另一仓库阻塞 |
| D6 | 前端拆分深度 | **分阶段，先更新流程 + API 客户端** | 控制风险；不引打包器 |
| D7 | 是否引入 jsdom/Playwright CI | **否** | 维护成本高于收益 |
| D8 | 备用配置门禁遇公告过期 | **放行（沿用最后一次成功结论）** | 可用性优先；该开关是资源保护而非急停 |
| D9 | 孤儿接管等待上限 | **3s** | 冷启动不能被 8s 拖慢 |
| D10 | 退出时壳侧等待上限 | **3s（发请求 1.5s）** | 用户不能看到"关不掉" |

---

## 附录 A：本轮实测记录

```
$ git rev-parse --short HEAD
0bc4cc0
$ git status --short
?? docs/design/
$ go test ./...
ok  cmd/freev6-helper   4.428s
ok  internal/mihomo    36.094s
ok  internal/network    2.907s
real 0m39.153s
$ node --test tests/*.test.js
# tests 32 / pass 32 / fail 0 / duration_ms 150.19
$ gofmt -l cmd internal | wc -l
28        # 96 个 .go 文件
$ git ls-files --eol internal/mihomo/shutdown.go
i/lf  w/crlf  attr/text=auto eol=lf
$ curl -sL .../releases/latest/download/version.json | head -c 3 | od -c
{  \r  \n      # 无 BOM；正文为 CRLF
```

基线（`0bc4cc0` 现场构建的 helper，临时空 root，端口 38111）的完整输出见
`docs/design/2026-11-28-baseline-test-record.md` §4；此处只留结论性数字：

```
boot p50=34ms (min 31 / max 37, n=8)
/status p50 ≈2.1ms   /runtime 2.1ms   /notice 274ms   /runtime/check 1.11s
/update/latest 1.02s / 0.48s   ?channel=beta 1.13s / 0.96s
守卫矩阵 401/401/403/403/403/204/405
PUT /update/download（缺 SHA）→ 200，≤1s 后 progress=error
```

## 附录 B：v1 → v2 差异对照

| v1 条目 | v2 处置 |
|---|---|
| §0.1 五个设计级问题 | 全部保留 |
| §2.2 Job 归 helper、验收改写、`MarkVerified(version)` | 保留 |
| §2.2 "shutdown 端点" | 补 3 条边界（Shutdown 超时、WithoutCancel、有界等锁）→ §2.2.6 |
| §2.2 "孤儿接管等 8s" | 改 3s → §2.2.3 |
| §2.2 "父 PID 无效则禁用监视" | 改为"env 缺失才禁用；env 在但进程打不开 ⇒ 视为父已退出，立即收尾" |
| §3 A1–A6 | 保留，补"`updateLatest` 回填缓存"（A4）与发布一致性规则（§4.4） |
| §3 B1–B8 | 保留，新增 B8（壳侧先等后杀）；会话锁补返回码判定（§2.2.7） |
| §3 C1–C3 | 保留，补锁序约束（C3） |
| §3 D1 | 改分层超时（§2.2.2） |
| §3 D5 | 补"经典脚本顶层 const 冲突"约束（§2.3.5） |
| §5 性能表 | 保留并补"每个新增等待的上界"（§5.1/§5.2） |
| §6 决策表 | 保留 7 条，新增 D8/D9/D10 |
| §3 A5 | 补"`PUT /update/download` 同步 400"——实测先回 200、1s 后才报错 |
| — | 新增 §1.4 改动前基线实测（启动 34ms / 更新检查 1.0s / 守卫矩阵）、`scripts/boot-measure` 作为批次 1 交付项 |
| — | 新增 F1（公告过期锁死备用配置）、E4（CRLF 门禁） |
