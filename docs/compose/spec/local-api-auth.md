---
feature: local-api-auth
status: delivered
updated: 2026-10-15
branch: feat/local-api-auth
commits: 9a562d3..141ee6e
---

# 本地 API 鉴权（per-launch bearer token）

## Report

**What was built** — freev6-helper 的全部 `/api/v1` 端点现在要求 per-launch bearer token：Tauri 壳在每次启动时用 RtlGenRandom 铸造 32 字节随机 token（十六进制 64 字符），经 `FREEV6_API_TOKEN` 环境变量传给 sidecar，前端通过新增的 `get_api_token` command 获取并随 `Authorization: Bearer` 头携带。helper 以 `withCORS(requireAuth(token, mux))` 包装：缺失/错误 token → 401，恒定时间比较，环境变量为空时拒绝所有请求（fail-closed）；CORS 预检仍在鉴权前放行，`Allow-Headers` 补上 `Authorization`、`Allow-Methods` 补上 `POST`。前端 `api()` 缓存 token，遇 401 自动重取并重试一次；脱离 Tauri 的浏览器场景拿不到 token，走既有「helper 离线」路径。token 每次启动轮换、不落盘、不写日志。

**Verification** — `go build ./...` 与 `go test ./... -count=1` 全绿（含新增 auth 中间件用例：无/错 token→401、正确→放行、OPTIONS 预检无 token→204、空服务端 token→全拒）；`cargo check` 通过；`node --check web/app.js` 通过。真实 helper 进程 + curl 冒烟：无 token 401、错 token 401、正确 token 200、预检 204 且 CORS 头含 Authorization/POST、无 `FREEV6_API_TOKEN` 时全部 401 并输出 stderr 警告。`cargo test` 因二进制带 requireAdministrator 清单在非提升会话无法执行（PRE-EXISTING），token 生成器以等价独立 rustc 程序验证（64 hex、两次调用不同）。独立 reviewer 审查 9a562d3..141ee6e：spec 合规、无 critical 缺陷。前端 Tauri invoke 路径未能端到端实跑（需启动提权桌面应用），仅静态走查。

**Journey log**:
- 会话环境禁止 `git worktree add`（隔离策略），按 spec 覆盖项改为在主检出就地建 `feat/local-api-auth` 分支实施。
- 首次 E2E 冒烟因本机已有 helper 占用 13335 失败；用户关闭占用程序后复测通过——验证真实部署路径时先确认端口占用者。
- `cargo test` 对带 requireAdministrator 清单的二进制在非提升 shell 必然 740；「独立 rustc 镜像程序」是本机验证 Rust 单测逻辑的可行替代。

## [S1] Problem

freev6-helper 的本地 API（`127.0.0.1:13335/api/v1`）当前完全无鉴权，且 CORS 为 `Access-Control-Allow-Origin: *`。任何浏览器页面都可以跨源调用它启动/停止免流、修改设置、触发运行时与应用更新下载、注册 WARP 设备。README TODO 与 roadmap M4.1 均把「本地 API 鉴权 / per-launch bearer token」列为发布前必做项。

## [S2] Design

**方案（用户已确认）**：每次桌面应用启动生成一次性随机 token，由 Tauri 传给 helper，前端经 Tauri command 获取并随请求携带。浏览器独立运行（无 Tauri）时按现有「helper 离线」状态处理，不提供 token 旁路。

### Token 生命周期

- Tauri 在 `setup` 中、启动 sidecar **之前**生成 token：32 字节 OS 随机数（Windows `RtlGenRandom` / `SystemFunction036`，与 main.rs 现有「裸 Win32 FFI、不动 Cargo.lock」惯例一致），十六进制编码为 64 字符。
- token 经环境变量 `FREEV6_API_TOKEN` 传给 helper sidecar（与现有 `FREEV6_ROOT` 同机制）。
- Tauri 把 token 存入 managed state，新增 command `get_api_token` 供前端读取；token 不落盘、不写日志。
- 每次应用启动轮换；孤儿 helper 接管场景下新 helper 携带新 token 杀掉孤儿后接管端口，不存在新旧 token 错配（第二实例因端口被占而 helper 退出 → 应用退出，为既有行为，不变）。

### Helper 侧校验（Go）

- `main()` 中以 `requireAuth(token, mux)` 包装整个 mux，再套现有 `withCORS`：`withCORS(requireAuth(token, mux))`。
- 校验规则：请求头 `Authorization: Bearer <token>` 与服务端 token 恒定时间比较（`crypto/subtle.ConstantTimeCompare`）。缺失、格式错误、不匹配 → `401` + `{"error": "unauthorized"}`。
- 服务端 token 取自 `os.Getenv("FREEV6_API_TOKEN")`。为空时**拒绝所有请求**（比较前要求服务端 token 非空，防止空对空放行），启动时向 stderr 打一行警告；helper 照常启动并绑定端口（不引入新的退出路径，孤儿接管、端口逻辑不变）。手动 `go run` 调试需自行设置该变量。
- CORS 调整（仅放宽，不收紧）：
  - `OPTIONS` 预检仍由 `withCORS` 在鉴权之前直接返回 204（预检不携带 Authorization）。
  - `Access-Control-Allow-Headers` 增加 `Authorization`（否则浏览器预检会拒掉带 token 的请求）。
  - `Access-Control-Allow-Methods` 补上 `POST`（现状只有 GET/PUT/OPTIONS，POST 端点一直在用，属潜在缺陷，顺带修正）。
- 所有 `/api/v1/*` 路由均注册在 mux 上，统一被中间件覆盖；未知路径同样 401（不泄露路由存在性以外的信息）。

### 前端（web/app.js）

- 所有 helper 请求已统一走 `api()`。`api()` 在发送前确保 token：存在 `window.__TAURI_INTERNALS__.invoke` 时调用 `get_api_token` 并缓存；invoke 不可用或失败 → token 为 null，请求不带 `Authorization`。
- 收到 `401` 时：强制重新获取 token 一次，若拿到的 token 与上次不同则用新 token 重试**一次**；仍失败或拿不到新 token → 抛错（走现有错误展示路径）。
- 浏览器独立运行时拿不到 token → 所有请求 401 → 现有轮询把它当作 helper 离线处理（用户已确认接受；日志中的 `unauthorized` 错误信息足够辨识）。
- mihomo controller（`127.0.0.1:9090`）的直连请求不经过 `api()`，本次不改。

### Rust 侧（src-tauri/src/main.rs）

- 新增 managed state `ApiToken(String)`；`setup` 中生成后 `app.manage(...)`，再 `start_helper`（spawn 时注入 `FREEV6_API_TOKEN`）。
- 新增 `#[tauri::command] fn get_api_token(state: State<ApiToken>) -> String` 并注册到 invoke_handler。
- 随机数生成封装为独立函数并配单元测试（长度 64、字符集为 hex、两次调用结果不同）。

## [S3] Out of Scope

- mihomo external-controller（`127.0.0.1:9090`）的 secret 鉴权（roadmap 独立条目）。
- Windows 命名管道替代 loopback HTTP（roadmap 终态方案）。
- Origin 白名单、DPAPI 保护 warp.json 等后续加固（roadmap 已列）。
- 浏览器独立运行时的 token 粘贴调试通道（用户已确认按离线处理）。
- 新建 git worktree：会话环境禁止 `git worktree add`，改为在主检出就地创建 `feat/local-api-auth` 分支实施（覆盖项，按此记录）。

## Tasks

- [x] T1: helper 鉴权中间件 + CORS 头修正 + 单元测试 — acceptance: `go test ./cmd/freev6-helper/` 全绿；测试覆盖无 token/错 token → 401、正确 token → 放行、OPTIONS 预检无 token → 204、服务端 token 为空 → 全部 401 (covers: S2)
- [x] T2: Tauri 侧 token 生成与下发（FFI 生成器 + managed state + `get_api_token` command + sidecar 注入 env）— acceptance: `cargo check` 通过；生成器单元测试通过（hex 64 字符、两次不同）(covers: S2)
- [x] T3: 前端 `api()` 注入 `Authorization` 并处理 401 重取重试一次 — acceptance: 代码走查确认所有 helper 请求经 `api()` 携带 token；无 Tauri 环境下行为与现有离线路径一致 (covers: S2; depends: T2)
- [x] T4: 文档同步：README TODO 勾选、src-tauri/README 中「still need per-launch authentication」表述更新 — acceptance: 两处文档与实际行为一致 (covers: S1)
