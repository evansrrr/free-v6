---
feature: security-hardening
status: in-progress
updated: 2026-10-16
branch: feat/security-hardening
commits:
---

# 本地安全加固：mihomo secret + Host/Origin 守卫

## Report

## [S1] Problem

本地鉴权（per-launch bearer token）上一轮已覆盖 helper API，但两个暴露面仍在：

1. **mihomo external-controller `127.0.0.1:9090` 无鉴权**：本机任意进程可读连接表/代理列表、切换代理节点（前端本来就直连它，是免流功能的一部分）。roadmap M2/M4 均列有「external-controller 加 generated secret」条目。
2. **helper API 对 DNS rebinding 与恶意网页直连无第二道防线**：token 是唯一门槛；Host/Origin 未校验（roadmap「strict origin checks」条款）。攻击者页面若以 `http://<evil>:13335` 反解析到回环地址，或伪造跨源请求，目前只能被 token 挡住。

已用本机真实 mihomo v1.19.31 实测：`secret` 配置生效（无/错 401、正确 Bearer 200），且 CORS 预检完整回显 `Allow-Headers: Authorization`、`ACAO: *`——前端带 Bearer 直连 9090 不会被预检拦截。

## [S2] Design

### [S2-A] mihomo controller secret

**Secret 存储**
- 文件 `state/mihomo-controller.secret`（0600，单行十六进制 32 字节），位于安装根 state 目录，与 `mihomo.yaml` 同级（该目录本就存放私钥材料，属既有私密边界）。
- 共享函数 `mihomo.LoadOrCreateSecret(stateDir string) (string, error)`：读到非空即用，缺失则 crypto/rand 生成并落盘。helper 与 CLI `freev6 start` 都调用它，保证两侧渲染出的配置一致。
- 每安装轮换一次（创建后不主动轮换；删除文件后下次启动重新生成）。跨应用重启保持一致——这是「新 helper 接管旧运行中 mihomo」场景不破功的关键。

**配置渲染**
- `RenderOptions` 增加 `ControllerSecret string`；非空时在 `external-controller: 127.0.0.1:9090` 行后注入 `secret: "<hex>"`。空值不注入（渲染结果与现状逐字节一致，回归零风险）。

**服务端调用方（Go）**
- `CheckController(ctx, address, secret)`、`WaitHealthy(ctx, address, secret, interval)` 增加 secret 参数；`getControllerJSON` 在 secret 非空时携带 `Authorization: Bearer <secret>`。`WaitReady` 是纯 TCP 探测，不改。
- 调用点更新：helper `startProxy`（用本轮渲染所用 secret）、CLI `freev6 start`（同）、相关测试。

**helper API 暴露**
- `GET /api/v1/status` 新增字段 `controllerSecret`（文件存在则返回，否则空串；请求本身已在 token 鉴权之后）。
- `POST /api/v1/proxy/start` 响应新增 `controllerSecret`（本轮实际渲染使用的值）。
- `startProxy` 在渲染前调用 `LoadOrCreateSecret`；读/写失败 → 500，发生在任何网络改动之前。

**前端（web/app.js）**
- `state.controllerSecret`：从 status 轮询与 start 响应两处填充；为空时不加头。
- 5 处 9090 直连（`/connections`、`/proxies`、代理切换 PUT、延迟测试）统一携带 `Authorization: Bearer <secret>`；mihomo 未配置 secret 时会忽略该头，因此「旧版启动的运行中实例 + 新前端」升级窗口不受影响。

### [S2-B] helper Host/Origin 守卫

- 新中间件 `guardLocalOnly`，链式位置：`withCORS(guardLocalOnly(requireAuth(mux)))`——OPTIONS 仍由最外层直接放行，守卫先于 token 校验（403 与 401 语义分明）。
- **Host 规则**：剥端口后必须是 `127.0.0.1` / `localhost`（可带端口），否则 403。挡 DNS rebinding（`Host: evil.com:13335`）。
- **Origin 规则**（倒挂式，稳定性优先）：缺省 → 放行（curl/CLI）；scheme 非 http/https → 放行（`tauri://…`、`null` 等非 Web 页面形态，真正的门禁仍是 token）；scheme 为 http/https 时 host 必须是 `127.0.0.1` / `localhost` / `tauri.localhost`，否则 403。恶意网页 origin 永不命中放行集，而 Tauri webview 的实际 Origin 无论取哪种形态都不会被误伤。
- 失败响应 `403 {"error": "forbidden"}`，不区分具体原因。

### 兼容与升级路径

- 存量运行实例（配置无 secret、无 secret 文件）：`controllerSecret` 返回空 → 前端不加头 → 旧 mihomo 照常应答；下次启动免流时渲染带 secret 的新配置，之后生效。升级窗口不断服。
- 用户手删 secret 文件而 mihomo 仍在跑：窗口内前端拿空串 → 9090 请求 401 → 面板数据暂空，停止/再次启动免流后自愈（接受的边界，用户篡改状态不投入更多防护）。

## [S3] Out of Scope

- DPAPI 加密 `warp.json`（用户已确认单独一轮做）。
- Windows 命名管道替代 loopback HTTP。
- helper CORS 头本身的调整（上一轮已完成）。
- mihomo `mixed-port` 7890 的访问控制（已 `allow-lan: false`，仅回环）。
- secret 落盘的 DPAPI 化/日志脱敏（roadmap「redact keys/tokens」条目，后续）。

## Tasks

- [ ] T1: `mihomo.LoadOrCreateSecret` + `RenderOptions.ControllerSecret` 注入渲染 + 单测 — acceptance: `go test ./internal/mihomo` 全绿；配置在 secret 非空时含 `secret:` 行、为空时与旧输出一致 (covers: S2-A)
- [ ] T2: `CheckController`/`WaitHealthy` 增加 secret 参数并携带 Bearer；更新 helper/CLI 调用点与测试 — acceptance: `go test ./...` 全绿；httptest 断言 secret 非空时请求头为 `Authorization: Bearer <secret>`、为空时不带该头 (covers: S2-A; depends: T1)
- [ ] T3: helper 接线：`startProxy` 注入 secret、status/start 响应带 `controllerSecret`、`guardLocalOnly` 中间件 + 单测 — acceptance: `go test ./cmd/freev6-helper/` 全绿；测试覆盖恶意 Host→403、外来 http Origin→403、tauri/缺省 Origin+正确 token→200、OPTIONS 仍 204、既有鉴权用例在守卫下不回归 (covers: S2-A, S2-B; depends: T1, T2)
- [ ] T4: 前端 `controllerSecret` 缓存与 5 处 9090 请求加 Bearer — acceptance: `node --check web/app.js` 通过；走查确认所有 9090 fetch 携带头、secret 为空时不加头 (covers: S2-A; depends: T3)
- [ ] T5: 端到端验证：真实 mihomo 二进制经渲染配置启动，curl 矩阵（无/错/对 secret、预检、Origin+Bearer）全符合预期；回归上一轮 helper 鉴权冒烟 — acceptance: 验证记录全 PASS (covers: S2-A, S2-B; depends: T3, T4)
