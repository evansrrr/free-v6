# 基线实测记录 — 批次 1 改动前（2026-11-28）

> 性质：**改动前基线**，不是批次 1 的验收报告。批次 1 的代码尚未实施（工作区在 `0bc4cc0`）。
> 配套文档：`2026-11-28-improvement-plan-v2.md`（下称 v2）。本记录是 v2 §1.4 与 §5.1"现状"列的原始证据。
> 本文件在批次 1 实施后**冻结不改**，改动后的对照结果另记。

---

## 1. 目的

v2 的性能目标此前只有"改后"数字，"现状"列多为代码推导（如"最坏 48s"）。本次实测要回答三件事：

1. 健康网络下这些路径**真实**耗时多少（上界与常态差多少）；
2. v2 对现状的判断（A5 先回 200 再报错、A4 缓存不回填、守卫矩阵）是否成立；
3. 给批次 1 的 T0 对照准备一组可复现的基准。

## 2. 环境

| 项 | 值 |
|---|---|
| OS | Windows 10.0.26300.9974（x64） |
| Go | go1.26.3 windows/amd64（注意：CI 用 1.25.x，见 v2 E1） |
| 代码基线 | `0bc4cc0`（`git status` 仅 `docs/design/` 未跟踪） |
| 被测二进制 | `go build -o <tmp>/freev6-helper.exe ./cmd/freev6-helper`（现场构建） |
| 运行方式 | `FREEV6_ROOT=<临时空目录>`、`FREEV6_API_TOKEN=<测试值>`、`FREEV6_API_PORT=38111` |
| 干扰控制 | **未触碰**本机正在运行的真实实例（`freev6-desktop` + 其 helper）；测试结束按 PID 精确结束测试进程，未用镜像名批量杀 |

## 3. 结果

| 指标 | 方法 | 结果 | 判定 |
|---|---|---|---|
| helper 冷启动 → `/status` 200 | Go 程序 spawn + 5ms 轮询，n=8 | min 31 / **p50 34** / mean 34 / max 37 ms | 通过；作为 +10ms 预算的基准 |
| `/status` | curl ×10 | 1.4–3.3 ms，p50 ≈2.1 ms | 无子进程、无 IO，符合预期 |
| `/runtime` | curl | 2.1 ms | 同上 |
| `/notice` | curl | 274 ms | 12 候选并发命中首个 |
| `/runtime/check` | curl | 1.11 s | 20s 上限只在退化时触发 |
| `/update/latest`（stable） | curl ×2 | 1.02 s / 0.48 s | 无缓存复用（0.5s 抖动即重复拉取的代价） |
| `/update/latest?channel=beta` | curl ×2 | 1.13 s / 0.96 s | 两通道串行，但健康网络下不显著 |
| 鉴权/来源守卫 | 7 组负例 | 401 / 401 / 403 / 403 / 403 / 204 / 405 | 全部符合 `auth.go` 设计 |
| 缺 SHA 的下载 | `PUT` + 轮询 | **PUT 200**，≤1s 后 progress=`error`「更新源校验失败：sha256 无效」 | 违背"错误尽早返回" → v2 A5 补同步 400 |
| 错 SHA 的下载 | `PUT` + 轮询 | PUT 200，≤3s 后 progress=`error`「更新地址与官方清单不匹配」 | 同上 |

## 4. 原始输出

```
$ go run ./bootmeasure <helper.exe> <temp-root>
run 1: 37 ms
run 2: 37 ms
run 3: 33 ms
run 4: 35 ms
run 5: 33 ms
run 6: 31 ms
run 7: 34 ms
run 8: 34 ms
min=31ms p50=34ms mean=34ms max=37ms

$ curl -w '%{time_total}' -H "Authorization: Bearer $T" http://127.0.0.1:38111/api/v1/status   # ×10
0.002105 0.001601 0.003323 0.002638 0.001547 0.001711 0.002314 0.001427 0.002519 0.002107

notice:        HTTP=200 time=0.274121
runtime/check: HTTP=200 time=1.108915
runtime:       HTTP=200 time=0.002109
update/latest: HTTP=200 time=1.022506
update/latest: HTTP=200 time=0.482081
beta #1:       HTTP=200 time=1.134146
beta #2:       HTTP=200 time=0.962856

no-token:   401      bad-token:   401      evil-origin: 403
null-origin: 403     evil-host:   403      options:     204
GET-on-start: 405

PUT download(no sha):  {"ok":true} HTTP=200
+1s 时 progress: {"done":0,"error":"更新源校验失败：sha256 无效","state":"error","total":-1}
PUT download(wrong sha): HTTP=200
+3s 时 progress: {"done":0,"error":"更新地址与官方清单不匹配","state":"error","total":-1}
```

线上资产（`api.github.com/repos/evansrrr/free-v6/releases/latest`）：仅 `FreeV6_0.5.4_x64-setup.exe` 与 `version.json`，**无 `.sig`** —— 签名过渡尚未开始。

## 5. 结论

1. **常态与上界差两个数量级**：更新检查常态 ~1.0s，48/96s 只在"每条线路都挂到 8s"时成立。收益表述必须分两栏，否则夸大。
2. **`/runtime/check` 1.1s、`/notice` 0.27s**：15s 客户端超时正常不误伤；但服务端预算是 20s/12s，**相等即必然先超时**，分层超时的理由成立（v2 §2.2.2）。
3. **启动只有 34ms**：`+<10ms` 是相对 34ms 的 +30%，必须同尺测量（v2 §5.3）。
4. **A5 的缺陷被实测确认**：错误入参先回 200，用户先看到进度条再看到红字（v2 §3.1 A5）。
5. **A4 的代价被实测量化**：健康网络多 0.5–1.1s 与 6 个请求；退化网络才到 96s。

## 6. 未覆盖（原因）

| 项 | 原因 |
|---|---|
| `/proxy/start`、`/proxy/stop` | 需要管理员 + TUN + 改系统网络状态，不在生产机上做；留给批次 2 真机矩阵 |
| 热点共享（ICS/WinRT） | 同上，且需要真实热点 |
| 退出/强杀收尾、Job 行为 | 批次 2 才实现 |
| 前端首屏 FCP/DCL | 需跑壳 + Chrome Performance；本轮未复测，沿用 2026-11-27 历史基线 |
| 镜像故障注入（48/96s 上界） | 需要改 hosts 或注入 URL 变量；改用 `httptest` 单测覆盖（v2 §6.3） |

## 7. 复现

1. `go build -o /tmp/fv6-baseline/freev6-helper.exe ./cmd/freev6-helper`
2. 起一个临时空目录作为 `FREEV6_ROOT`，设 `FREEV6_API_TOKEN`（任意 64 hex）与高位 `FREEV6_API_PORT`。
3. 启动工具：spawn 子进程后每 5ms `GET /api/v1/status`（带 Bearer），首个 200 记时，跑 8 次取 p50；结束按 PID kill。
   （批次 1 将把它固化为 `scripts/boot-measure/main.go`，见 v2 §5.3。）
4. 其余指标用 `curl -w '%{time_total}'`，串行执行，避免相互抢占。
