# 批次 2 验证记录（生命周期与会话所有权）

> 性质：批次 2（`2026-11-28-batch2-plan.md` 的 B1–B9）**改动后**的验证记录。
> 代码基线：批次 2 实施提交（在 `7535b0f` 之后）。
> 未执行：真机矩阵 T1–T10 里需要"真实免流会话"的项（T1/T2/T4/T5/T7）、混合版本互换（T8）、`cargo test`（二进制带 requireAdministrator 清单，未提权时无法执行）。

---

## 1. 交付物

| 文件 | 说明 |
|---|---|
| `internal/mihomo/job{.go,_windows.go,_other.go}` | helper 自持的 kill-on-close Job；自加入失败记日志并退化为"只挂核心" |
| `internal/mihomo/sessionlock_{windows,unix}.go`（原 `startlock_*`） | 会话锁改名并扩展语义；**对象名不变**；有界等待 60s；校验 `WaitForSingleObject` 返回码 |
| `internal/mihomo/process.go` | `Start` 在写 pid 前把核心挂进 Job（自加入成功时幂等） |
| `internal/mihomo/corestore.go` | `MarkVerified(version) bool`；`Prune` 只在 previous 文件确实不在时清指针 |
| `internal/lifecycle/stop.go` | 共用 `StopSession`，顺序固定为 还原→关TUN→停核→重读→清路由→删快照 |
| `cmd/freev6-helper/{shutdown,parent_windows,parent_other}.go` | `/api/v1/shutdown`、父进程监视、收尾只执行一次 |
| `cmd/freev6-helper/main.go` | 注册端点、启动建 Job、`stopProxy` 改走共用流程、`MarkVerified(core.Version)` |
| `cmd/freev6/main.go` | CLI `stop` 改走共用流程（补关 TUN，读 controller secret） |
| `src-tauri/src/helper_client.rs` | 标准库手写 HTTP/1.1，只发一个 POST，可配超时 |
| `src-tauri/src/main.rs` | 注入 `FREEV6_PARENT_PID`；`update_apply` 优雅关停 + 门禁；`Exit` 有界等待；`reap_stray_helpers` 先等后杀 |

## 2. 静态验证

| 命令 | 结果 |
|---|---|
| `go build ./...` | 通过 |
| `go test -count=1 ./...` | 全绿；helper 5.68s、lifecycle 1.41s、mihomo 36.29s、network 2.46s |
| `go vet ./...` | 通过 |
| `staticcheck ./...`（v0.8.2，与 CI 同版本） | 通过（清理了停止流程迁走后失效的两个超时常量） |
| `gofmt -l`（改动文件） | 空 |
| `node --check` / `node --test` | 通过，32/32 |
| `cargo check --locked` | 通过 |
| `cargo check --locked --tests` | 通过（测试代码可编译） |
| `cargo test` | **无法执行**：测试二进制带 requireAdministrator 清单（os error 740），与仓库既有说明一致 |

## 3. 单元测试

`internal/lifecycle`（7 个）：顺序断言（restore < lock < tun < core < routes < remove）；还原失败仍停核且保留快照；停核失败不清路由、不删快照；停止窗口内新会话接管 → 跳过清理；适配器未消失仍继续停核；核心未运行时不碰 TUN；拿不到锁时一个系统状态都不动。

`cmd/freev6-helper`（4 个）：`GET` 拒绝（405）；`h.mu` 被持有时返回 409；收尾只执行一次（重复调用仍 200，但不重复退出）；父监视路径同样只收尾一次并退出。

`internal/mihomo`（新增 1 个）：`MarkVerified` 只接受当前 active 的版本，空版本与其它版本一律不标记。

## 4. 实机验证（Windows 10.0.26300）

用一个一次性 Go 探针（已删除，不入库）验证三件事：

| 项 | 方法 | 结果 |
|---|---|---|
| Job 自加入 | 启动 helper 后对该 PID 调 `IsProcessInJob` | **inJob=true** |
| `shutdown` 端点（原始字节） | 用与 `helper_client::build_request` **完全相同**的字节串发到 127.0.0.1 | **status=200**，随后进程退出 |
| 父进程监视 | 起一个 dummy 父进程，helper 带 `FREEV6_PARENT_PID` 启动，然后杀掉父进程 | helper **23ms** 内退出 |
| 启动耗时 | `scripts/boot-measure`，同一临时 root 条件，n=15 | 批次 1 p50 **29ms** → 批次 2 p50 **30ms**（+1ms，预算 +5ms） |

补充：`beta-channel` 已指向 `v0.5.7-beta.1`（`version.json` + `.sig`），指针内容用 `freev6-update-sign -verify` 验过（`signature OK (key id 2026-11)`）。

## 5. 未覆盖与残余风险

| 项 | 原因 | 影响 |
|---|---|---|
| T1/T2/T4/T5/T7（带真实核心的停止、杀壳、杀 helper、更新、CLI） | 需要管理员权限 + TUN + 真实网络，且会改动本机网络状态 | 由用户在有管理员权限的会话里按计划 §8.2 执行 |
| Job 级联杀核心 | 未启动真实核心 | 依赖 `IsProcessInJob=true` + `KILL_ON_JOB_CLOSE` 的 OS 语义；自加入失败时的"只挂核心"分支未在实机触发 |
| T8 混合版本互换 | 需要两套二进制 | 代码层按 §5.1 降级，未实测 |
| `cargo test` | 需提权 | 新增的 4 个 Rust 用例只验证了可编译 |

## 6. 回滚

批次 2 的改动集中在 helper 与壳的退出路径，回滚只需回退到本批之前的提交；`/api/v1/shutdown` 是新增路由，旧二进制收到 404 时壳侧会回落 `/proxy/stop` + 强杀，不存在数据格式升级。
