# 批次 1 验证记录（2026-11-28）

> 性质：批次 1（更新信任链 + 时延）**改动后**的验证记录。
> 对照基线：`2026-11-28-baseline-test-record.md`（改动前，冻结不改）。
> 计划依据：`2026-11-28-improvement-plan-v2.md` §6（A1–A6）。
> 未在本轮验证：真实签名发布（需要 GitHub Secret 与仓库公钥）、内核清单签名（跨仓库）、`-race`（本机无 cgo，由 CI 的 go-race job 覆盖）。

---

## 1. 交付物

| 文件 | 说明 |
|---|---|
| `internal/appupdate/{keys,verify,sign}.go` + `update_public.pem` | 公钥表（PEM 可多块）、信封 `v1.<keyId>.<b64>` 验签、签名与密钥 PEM 编解码 |
| `cmd/freev6-update-sign/main.go` | `-gen-key` / 签名 / `-verify`；私钥只从文件读；原子写；拒绝覆盖已存在私钥 |
| `cmd/freev6-helper/update.go` | 候选并发 + 15s 预算、成对取清单与签名、先验签后解析、缓存回填、同步 400 |
| `cmd/freev6-helper/update_test.go` | 新增 9 个用例（成对/缺签名/预算/并发/缓存/同步 400） |
| `web/app.js` | 缺 sha256 不再发起下载（原"跳过校验"分支删除） |
| `.github/workflows/release.yml` | 签名 → 本机回验 → 先 `.sig` 后清单 → 拉回来再验 |
| `.github/workflows/beta-channel.yml` | 镜像 `.sig`，顺序同上，上传后回验 |
| `docs/release.md` | 密钥生成/备份/轮换/丢钥应急 |
| `scripts/boot-measure/main.go` | 启动耗时基准工具（§5.3 的那把尺子） |
| `.gitattributes` | `*.sig -text`、`version.json -text` |

## 2. 静态验证

| 命令 | 结果 |
|---|---|
| `go build ./...` | 通过 |
| `go vet ./...` | 通过 |
| `staticcheck ./...`（v0.8.2，与 CI 同版本） | 通过（修掉一处 ST1008 返回值顺序） |
| `gofmt -l` 新增/改动文件 | 空 |
| `node --check web/app.js` | 通过 |
| `node --test tests/*.test.js` | 32/32 通过（146ms） |
| `go test -count=1 ./...` | 全绿，墙钟 **38.8s**（基线 39.2s） |

包级耗时（`-count=1`）：helper 6.088s（基线 4.428s，新增用例约 +1.7s）、appupdate 0.635s、update-sign 0.671s、mihomo 36.581s、network 2.883s。

## 3. 单元测试明细

`internal/appupdate`（10 个）：有效签名通过；篡改一字节拒绝；未知 key 拒绝；7 种畸形信封拒绝（空/缺段/错版本/空 id/非 base64/截断/多余后缀）；空公钥表失败关闭；省略 keyId 时用指纹；公钥 PEM 往返（同时登记 Key-Id 与指纹两套 id）；注释-only 文件不 panic；私钥 PEM 往返；签名入参校验（空清单/空私钥/id 含点号或空格）。

`cmd/freev6-update-sign`（3 个）：生成→签名→回验全链路（含清单改一字节必须失败）；拒绝覆盖已存在的私钥；参数缺失/多余必须报错。

`cmd/freev6-helper` 新增（9 个）：

| 用例 | 断言 |
|---|---|
| `TestUpdateLatestBudgetWithHungMirror` | 一条挂死 + 两条可用时，可用线路立即胜出（0.00s）；三条全挂时由预算兜底（0.82s ≈ 800ms 预算） |
| `TestUpdateLatestFetchesChannelsConcurrently` | stable/beta 各挂 200ms，总耗时 210ms（串行会是 400ms+） |
| `TestUpdateLatestPopulatesVerifiedCache` | 检查更新后，源校验不再发请求（命中次数不变） |
| `TestFetchVerifiedManifestRejectsPairMismatch` | 清单来自 A、签名覆盖 B → 拒绝 |
| `TestFetchVerifiedManifestRejectsUnsigned` | 缺 `.sig`（404）→ 拒绝，错误里点名 missing signature |
| `TestVerifyUpdateSourceColdCacheBudget` | 冷缓存两路全挂 → 300ms 预算内返回（0.30s） |
| `TestUpdateDownloadRejectsMissingSHA` | 缺 SHA / 短 SHA / 白名单外地址 → 同步 400，且 progress 保持 idle |
| `TestVerifyUpdateSource`（重写） | 命中通过、不命中拒绝、畸形拒绝 |
| `TestUpdateLatestHandlerFlow`（重写） | 签名清单正常返回；不可达时 `update:false` |

## 4. 真机验证（Windows 10.0.26300，Go 1.26.3）

对象：`go build` 现场产出的 `freev6-helper.exe`，`FREEV6_ROOT` 指向临时空目录，端口 38311/38312。真实运行中的实例全程未被触碰，测试进程按 PID 精确结束。

| 项 | 方法 | 结果 | 判定 |
|---|---|---|---|
| 冷启动 → `/status` 200 | `scripts/boot-measure -n 8`（预热 3 次后） | min 28 / **p50 31** / mean 31 / max 34 ms | 基线 34ms，无回归 |
| 线上未签名清单 | `GET /update/latest`（真实 GitHub 镜像族） | `{"update":false,"error":"missing signature: unexpected status: 404"}`，**1.24s** | 失败关闭生效；并发后不再等 48s |
| beta 通道 | `GET /update/latest?channel=beta` | 200，0.72s，`update:false` | 两通道并发，无叠加等待 |
| 缺 SHA 下载 | `PUT /update/download` | **400**「缺少有效的 sha256，已拒绝下载」 | 旧实现是 200 + 后台报错 |
| 白名单外地址 | 同上，`https://evil.example/x.exe` | **400**「下载地址不在允许范围」 | 同步拒绝，不起 goroutine |
| 下载状态 | `GET /update/progress` | `state=idle` | 被拒绝的请求没有污染下载状态机 |
| 鉴权/来源守卫 | 7 组负例 | 401 / 401 / 403 / 403 / 403 / 204 / 405 | 与基线完全一致 |
| 签名工具端到端 | `-gen-key` → 追加公钥 → `-in/-out` → `-verify` | `signature OK (key id 2026-11, 74 bytes manifest)` | 往返正确 |
| 未配置公钥时 | 恢复占位文件后再 `-verify` | `验签失败: 未内置更新公钥`，exit 1 | 失败关闭；发布的"回验"步骤会因此拒绝出包 |

测试结束后已恢复 `update_public.pem` 占位内容，临时私钥目录已删除。

> 说明：上面两轮测试的 helper 都是按 PID `taskkill` 结束的，任务运行器会把这次**主动终止**记为 exit code 1；实测两次的 stderr 均为空、端口已释放、系统里只剩用户自己的实例，不是崩溃。

## 5. 与基线对照

| 指标 | 基线 | 改动后 | 结论 |
|---|---|---|---|
| helper 冷启动 p50 | 34 ms | **31 ms** | 无回归（噪声范围内） |
| 更新检查（健康网络，未签名清单） | 1.02 s（返回“有更新”） | 1.24 s（返回“无更新”） | 多一次 `.sig` 请求；语义按设计反转 |
| 更新检查（全线路挂死） | 最坏 48 s（推导） | 单测断言 ≤ 预算（800ms 压缩后实测 0.82 s） | 预算生效 |
| 点更新后的源校验 | 冷缓存最坏 96 s（推导） | 命中缓存 0 请求；冷缓存 ≤ 10 s（单测 0.30 s 压缩预算） | 生效 |
| 全量测试墙钟 | 39.2 s | 38.8 s | 无回归 |

## 6. 未覆盖与阻塞

| 项 | 原因 | 谁来做 |
|---|---|---|
| T4–T6（旧客户端升级、投毒演练、指针恢复） | v0.5.7-beta.1 已于 2026-11-28 09:10Z 发布且签名/安装包 SHA 均核对通过，`beta-channel` 指针也已切到该版本并通过验签；但清单里的安装包名与实际上传资产不一致（见 §9），应用内下载仍会 404 | 由 `v0.5.7-beta.2` 修复命名后重跑 |
| 内核清单签名（A2） | `f6-core` 仓库发布流程需单独改 | 后续批次 |
| `-race` | 本机无 cgo | CI `go-race` job |
| 前端首屏 FCP/DCL | 需要跑壳 + Chrome Performance | 批次 4 |

## 7. 回滚

代码回滚只能回到"已带验签的客户端"之后的版本，且回滚后的版本**仍须产出签名清单**（否则新客户端拒绝更新）。`.sig` 是纯增量资产，留在 release 上无副作用。

## 8. 批次 1.1 补丁验证（2026-11-28 晚）

按下述三条复核意见打了补丁（另加两条流水线加固），逐条验证：

| 补丁 | 验证 | 结果 |
|---|---|---|
| 清单/签名请求补齐重定向守卫（`redirectGuard`，与下载路径共用） | `TestFetchBodyRejectsRedirectOutsideAllowlist`（127.0.0.1 → localhost 被拒）、`TestFetchBodyFollowsAllowlistedRedirect`（同主机跳转仍可用） | 通过 |
| 同上：不打断真实链路 | 临时用例走真实镜像链拉 `v0.5.7-beta.1` 的清单：`fetchVerifiedManifest` → `version=0.5.7-beta.1`，**0.74s** | 通过（GitHub → CDN 的 302 仍在白名单内） |
| 前端改发原始 github.com URL | `node --check` + 32/32 前端测试；helper 侧 `updateDownloadCandidates` 对 github.com 才展开镜像（单测 `TestUpdateDownloadCandidatesRewritesGitHubURL` 覆盖） | 通过（真机多镜像下载待下次更新演练） |
| 发布流水线上传后校验安装包 SHA | YAML 解析通过；`Get-FileHash` 对比清单 `sha256`，不一致即失败 | 待下一次发布执行 |
| beta 指针定时对账（4 次/天）+ 上传前验签 + 幂等 | YAML 解析通过；解析目标 tag 用 `gh release list` 过滤 `beta-channel` 自身 | 待下一次 `schedule` 或手动 dispatch |

全量回归：`go test -count=1 ./...` 全绿、`go vet`、`staticcheck v0.8.2`、`gofmt`（改动文件）、`node --test` 32/32 均通过。

未验证：`beta-channel` 指针仍未更新（需要手动 dispatch 或等定时任务）；投毒演练（T5）与旧客户端升级（T4）仍待指针修好后执行。

## 9. 批次 1 收尾：安装包命名事故（2026-11-28 晚）

`v0.5.7-beta.1` 发布后，外部复核实测到一个发布阻断：

| 项 | 值 |
|---|---|
| 签名清单 | `setup = FreeV6_0.5.7_x64-setup.exe`，`download = .../v0.5.7-beta.1/FreeV6_0.5.7_x64-setup.exe` |
| 实际资产 | `FreeV6_0.5.7-beta.1_x64-setup.exe` |
| 后果 | 5 个镜像 + 直连全部 404；前端"发现更新 → 点击 → 下载失败" |
| 同一份清单里 `sha256` | 与实际资产**一致**（只是文件名不同） |

根因不是 helper 或前端：手工改名发生在生成清单之后，而签名清单已经发布，改资产名
等于让清单指向不存在的文件。批次 1.1 新增的"上传后按清单名回下载并核对 SHA"会在
下一次发布时挡住这类错误，但 `v0.5.7-beta.1` 是在该检查之前构建的。

已修复（`v0.5.7-beta.2`）：

1. 流水线构建后统一改名 `FreeV6_<完整版本>_x64-setup.exe`：正式版名字不变，测试版
   一定带 `-beta.N`；清单、上传、attestation 使用**同一份路径**，不再各自猜。
2. 构建前清空 `bundle/nsis/*-setup.exe`，杜绝取到上一轮的同名文件。
3. 断言产物名必须包含完整 tag 版本，否则构建失败（正式版/测试版不可区分即失败）。
4. `beta channel` 推指针前核对"清单声明的安装包确实是该 release 的资产"，对不上
   就拒绝推进指针。

T3 的验收口径随之改成：**从签名清单的 `download` URL 实际下载安装包并核对
`sha256`**，而不只是验 `.sig` 与资产存在。
