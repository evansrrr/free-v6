# 批次 3 验证记录（配置一致性 · 前端韧性 · 公告门禁）

> 环境：Windows 10.0.26300，Go 1.26.3，现场构建的 helper（临时 root / 高位端口 /
> 隔离 `LOCALAPPDATA`），未触碰本机正在运行的真实实例。
> 本轮不打 tag；已发布的 `0.5.7-beta.2` 行为不变。

## 1. 交付物

| 项 | 位置 | 结果 |
|---|---|---|
| C1 原子写 | `chain.go`、`cloudconfig.go` | 两处 `os.WriteFile` → `writeFileAtomic` |
| C1/C3 配置事务锁 | `main.go`（`configMu`）、两个 import handler | 互斥检查 + 写内容 + 翻开关在同一把锁内；锁序 `configMu → settingsMu` 写进注释 |
| C2 reset 顺序 | `main.go` | 先写默认设置（`overwriteSettings`），再删内容文件；删除失败只记日志 |
| G1 reset 闭环 | `main.go` | reset 不再走 `updateSettings`，损坏的 `settings.json` 也能被覆盖 |
| D1 分层超时 | `web/lib/api-timeouts.js`（新）、`web/app.js` | 按服务端预算分档；超时不再回落 13335；JSON 解析失败给干净错误 |
| D2 节点列表韧性 | `web/app.js` | `fetchProxies` 3s 上限；`switchProxy` 校验 `resp.ok`、失败不回填 |
| F1 门禁与展示拆分 | `notice.go`（`loadNoticeGate`）、`cloudconfig.go` | 7 天过期只影响展示，不再把"没拿到新公告"当成"公告说不行" |
| G2 互斥 | `chain.go`、`cloudconfig.go` | 两个导入入口在锁内检查对方开关，冲突 409 |

## 2. 静态与单元测试

| 项 | 结果 |
|---|---|
| `go build ./...` / `go vet ./...` / `staticcheck v0.8.2` | 通过 |
| `go test -count=1 ./...` | 全绿；helper 6.69s、mihomo 36.46s、lifecycle 1.52s |
| `node --check web/app.js`、`node --check web/lib/api-timeouts.js` | 通过 |
| `node --test tests/*.test.js` | **37/37**（新增 5 个时延档位用例） |
| `cargo check --locked` | 通过（本批未改 Rust，回归） |

新增 Go 用例（`config_consistency_test.go`）：

| 用例 | 断言 |
|---|---|
| `TestResetRecoversFromCorruptSettings` | 损坏的 `settings.json` → reset **200**，且文件恢复为可解析的默认值 |
| `TestResetStaysSuccessfulWhenContentCannotBeRemoved` | 内容文件删不掉（非空目录）→ reset 仍 200，`chainEnabled=false` |
| `TestChainImportRejectsEnabledCloudProfile` | 备用配置开着 → 导入链式 **409**，且不落盘 |
| `TestCloudImportRejectsEnabledChain` | 反向同样 **409** |
| `TestConcurrentImportsKeepOneOutlet` | 并发导入只有一个成功，两个开关不会同时为 true |
| `TestCloudGateKeepsConclusionAfterCacheExpiry` | 30 天前的正结论仍放行；负结论仍锁定；无缓存仍锁定 |
| `TestExpiredCacheStillHiddenFromNotices` | 展示语义不变：8 天前的缓存不出现在公告里 |

前端用例（`tests/api-timeouts.test.js`）：每个档位、默认值、前缀不误伤
（`/proxy/startup`、`/noticeboard`），以及"客户端超时 ≥ 服务端预算 ×1.5"
这条关系本身。

## 3. 真机验证

### 3.1 T4 · 更新链路（beta 通道，真实发布资产）

| 步骤 | 结果 |
|---|---|
| `GET /update/latest?channel=beta` | `update:true`，`beta.version=0.5.7-beta.2`，0.71–0.79s |
| `PUT /update/download`（清单 URL + SHA） | 200，`state=done` |
| 落盘校验 | 7,944,171 字节，`sha256=2b5bcf9f69b4c31e…` 与清单一致 |
| 通道形状 | stable 为空（0.5.4 早于签名，验签不过 → 失败关闭），beta 正常；前端按 `preview` 只比较 beta |

> 结论：命名修复后清单 URL 可直接下载；"旧客户端升级"的下载环节成立。真正的
> NSIS 覆盖安装未执行（会替换本机正在运行的实例）。

### 3.2 T5 · 投毒演练（真实签名的工件）

| 动作 | 结果 |
|---|---|
| 指针 `version.json` + `.sig` | `signature OK (key id 2026-11, 2940 bytes manifest)` |
| 改一个字段（`download` 里的 beta.2 → beta.9） | 验签失败 |
| 用 beta.2 的清单配 beta.1 的签名 | 验签失败 |

### 3.3 T6 · 指针一致性

`beta-channel` 已指向 `0.5.7-beta.2`：`setup=FreeV6_0.5.7-beta.2_x64-setup.exe`、
`sha256=2b5bcf9f…`，签名通过。新守卫在 beta.1 上实测会拒绝推进
（`ASSET MISSING -> guard fails`），指针不会再指向"清单声明了但资产不存在"的版本。

> 未执行：删除/破坏指针后的自动恢复需要写权限，只验证了幂等分支与负向守卫。

### 3.4 批次 3 行为

| 场景 | 结果 |
|---|---|
| `settings.json` 损坏 → `PUT /settings` | 500 `settings-corrupt`（保护语义保留） |
| 紧接着 `POST /settings/reset` | **200**，返回默认值；`GET /settings` 可解析 |
| 30 天前的缓存 + `cloudProfile=true` → `GET /cloud-config` | `locked:false`（旧实现会锁定） |
| 同缓存改成 `cloudProfile=false` | `locked:true` |
| 无缓存 | `locked:true`（fail-closed 不变） |
| 备用配置已启用 → `PUT /chain-proxy` | **409** `链式代理与备用配置只能启用一个，请先关闭另一个` |

## 4. 未覆盖与残余风险

| 项 | 原因 | 影响 |
|---|---|---|
| 前端 `api()` 超时的真实浏览器验证 | 需要 WebView2 会话 + 一个会挂死的 helper | 档位表由单测覆盖；`AbortController` 是标准 API |
| 慢接口的端到端超时（`/proxy/start` 180s） | 需要真实免流会话 | 只在拒绝路径上改变行为 |
| 内容文件"写一半"的崩溃注入 | 无法在真实文件系统上稳定复现 | 原子写本身是标准做法，且有 `writeFileAtomic` 既有测试 |
| F1 的维护者侧生效延迟 | 需要客户端离线 > 7 天 | 已作为取舍记录（资源保护开关，非急停） |

## 5. 回滚

三组改动互不依赖，可单独回退；没有数据格式变更（`settings.json` 与内容文件格式
不变），旧版本可直接读取新版本写出的文件。

## 6. 外部复核后的收尾（2026-11-28 晚）

独立复核确认了主干（配置事务、分层超时、reset、门禁）并复跑了全部门禁，同时指出
"事务的最后两个入口"没覆盖。本轮补完：

| # | 问题 | 修复 |
|---|---|---|
| P1 | `chainClear`/`cloudClear` 先删内容再关开关，且不取 `configMu`；崩在两步之间 = "开 + 无内容" | 两个 clear 进 `configMu`，**先关开关再删内容**，删除失败只记日志（与 reset 一致） |
| P1 | `startProxy` 读 settings 与内容文件时不在 `configMu` 内，与导入/清除有一个分裂窗口 | 读取段（settings + 两个文件）进 `configMu`，解析与长操作在锁外；锁序 `h.mu → configMu → settingsMu` |
| P1 | `/proxy/start` 的 180s 小于真实最坏（多段之和 ≈400s） | 档位拆开：start **420s**、stop **200s**；测试改为"覆盖各阶段之和 + 10s"而不是对单段套 ×1.5 |
| P2 | `/warp/register` 落默认 10s，小于 WARP 客户端 20s | 新增 35s 档，并写进预算表测试 |
| P2 | `parentWatchLockBudget=150s` 小于 start 的真实持锁时间 | 提到 **420s**：父进程在长启动中消失时，先让启动跑完再收尾，而不是半途退出把热点留在半切换 |
| P2 | `cloudImport` 回传的 `updatedAt` 是导入前的值（首次为空） | 用本次写入的时间，回传值与持久化一致 |
| P3 | `lib/api-timeouts.js` 加载失败时兜底 15s，start/stop 会比旧版更糟 | 兜底按路径给 200s（start/stop），其余 10s |

未采纳（本轮）：

- **Rust 纯用例抽成可 `cargo test` 的 lib 目标**：需要把 `classify_shutdown` /
  `helper_client` 移出 bin crate，属于壳侧结构调整，留给批次 4 与前端拆分一起做。
- **`api()` 的 AbortError/不回落/204 行为测试**：`api()` 直接依赖 `fetch`/DOM，
  需随批次 4 把它抽成可注入的模块再测。本轮只覆盖了超时表。

### 6.1 本轮真机验证

| 场景 | 结果 |
|---|---|
| 导入 → 清除链式代理 | `import=200 enabled=true count=1` → `clear=200 enabled=false count=0`，`config/` 只剩 `settings.json` |
| 备用配置首次导入 | `updatedAt=2026-11-28T20:02:27+08:00`（非空，与持久化一致） |
| 30 天前的正结论 | `locked:false`，导入 200（F1 仍生效） |

门禁：`go test -count=1 ./...` 全绿（helper 新增 4 个用例）、前端 **38/38**、
`go vet`、`staticcheck v0.8.2`、`gofmt`、`cargo check --locked` 全部通过。
