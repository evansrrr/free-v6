# FreeV6 项目交接文档

> 对应代码：`main` @ `f609889`（2026-11-28）
> 已发布版本：`v0.5.7-beta.2`（预发布，beta 通道指针指向它）
> 读者：接手维护的开发者。读完应能独立完成「改代码 → 跑门禁 → 发版 → 排障」。
> **本文件不含任何密钥**。私钥、Secret 的交接见 §13。

---

## 0. 一分钟版本

FreeV6 是 Windows 桌面的「教育网 IPv6 免流」客户端：Tauri 2 壳（Rust）+ Go 助手（提权，
持有网络操作）+ mihomo 内核（`f6-core`，由助手拉取/校验/启动）。前端是无打包器的静态
HTML/JS，通过 loopback HTTP 调助手。

```text
freev6-desktop.exe  ──spawn──▶  freev6-helper.exe  ──spawn──▶  f6-core（mihomo）
   (UI / 托盘 / 更新)              (提权 / 本地 API :随机端口)        (TUN / :9090 控制口)
        │                                   │
        └──── HTTP + Bearer token ──────────┘
```

改完代码的最小门禁（Windows 本机）：

```powershell
go test -count=1 ./... ; go vet ./... ; go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
node --check web/app.js ; node --test tests/*.test.js
cd src-tauri ; cargo check --locked ; $env:FREEV6_TEST_MANIFEST=1 ; cargo test --locked
```

---

## 1. 系统与边界

### 1.1 它做什么

- 在校园网内启动一条经 Cloudflare WARP/MASQUE 的隧道（`f6-core` 内核 + TUN 适配器
  `FreeV6TUN`），只让「不计流量」的 IPv6 目标走隧道。
- 把这条隧道通过 Windows 移动热点共享给手机等设备（ICS 拓扑切换，可回滚）。
- 链式代理 / 备用配置两种「自定义出口」，互斥，导入后落盘。
- 应用内更新（带 Ed25519 签名校验）与内核更新（`f6-core` 仓库）。

### 1.2 平台与依赖

| 项 | 值 |
|---|---|
| 目标系统 | Windows 10 x64（需 WebView2）/ Windows 11 x64 |
| Go | `go.mod` = 1.25.0；CI 用 `1.25.x`；本机 1.26.3 也可 |
| Rust | stable（`dtolnay/rust-toolchain@stable`） |
| tauri-cli | `2.11.4`（`release.yml` 的 `TAURI_CLI_VERSION`） |
| Node | 20（仅测试与语法检查，不参与打包） |
| 第三方依赖 | Go：`yaml.v3`、`x/sys`；Rust：Tauri 2 全家桶 |

### 1.3 三个进程

| 进程 | 来源 | 权限 | 职责 | 退出语义 |
|---|---|---|---|---|
| `freev6-desktop.exe` | `src-tauri/` | **requireAdministrator**（生产清单） | 窗口/托盘/热键/更新编排/拉起助手 | 托盘退出 → 先请助手收尾，再退 |
| `freev6-helper.exe`（sidecar） | `cmd/freev6-helper` | 继承提权 | 全部网络副作用、本地 API、内核管理 | `/shutdown` 或父进程消失 → 停会话后退出 |
| `f6-core-*.exe` | `<root>/runtime/cores/` | 继承提权 | mihomo 内核，TUN + 控制口 9090 | 被助手停止，或随助手的 Job 一起被杀 |

**边界原则**：UI 不碰 WARP 私钥、不碰网络配置、不直接杀内核；所有特权动作只在 helper。
UI 只认 loopback HTTP + Bearer token。

---

## 2. 代码地图

```text
cmd/
  freev6-helper/          助手（生产主体）：main.go 1457 行 + 20 余个模块
    main.go               路由注册、startProxy/stopProxy、鉴权/守卫、settings
    update.go / update_test.go   应用更新清单、镜像候选、验签、下载（771 行）
    cloudconfig.go        备用配置导入/清除/门禁
    chain.go              链式代理导入/清除/订阅拉取
    shutdown.go           POST /shutdown、父进程监视入口、共用停止
    parent_windows.go     父进程句柄监视（Windows）
    notice.go             公告拉取/缓存/门禁读取
    auth.go               Bearer token、Origin/Host 守卫
  freev6/                 CLI（start/stop/status），与 GUI 共用停止流程
  freev6-update-sign/     签名工具：-gen-key / 签名 / -verify
internal/
  appupdate/              Ed25519 验签库 + 内置公钥 update_public.pem
  mihomo/                 内核发现/启动/停止、CoreStore（版本/回滚位）、更新检查
    job*.go               进程 Job：kill-on-close，核心随助手退出
  network/                TUN、ICS 热点、快照/还原、残留路由、权限检测
  lifecycle/              StopSession：唯一停止顺序（GUI/CLI/退出共用）
  warp/                   WARP 注册 + MASQUE 凭据（x25519）
  stallhttp/              停滞检测的 HTTP 包装
web/                      前端（无打包器）
  app.js                  4079 行，全部业务逻辑
  lib/{version,palette,host,api-timeouts}.js   UMD 小模块（node:test 可测）
  index.html              815 行，单页 + CSP
  traffic.js / ipv6gate.js / halloween.js     经典脚本
src-tauri/
  src/main.rs             1253 行：窗口/托盘/热键/单实例/helper 生命周期/update_apply
  src/helper_client.rs    最小 HTTP 客户端（只发一个 POST /shutdown）
  nsis-hooks.nsh          安装前检查「应用/助手是否还在跑」
  windows-app-manifest.xml  requireAdministrator（测试构建替换为 asInvoker）
scripts/                  boot-measure（启动基准）、check-icons.py、subset-icons.py
tests/                    node:test（38 个用例）
docs/                     见 §12 的文档索引
```

规模：Go 112 个文件 / 269 个 `Test*` 函数 / 约 2.4 万行（含测试）。

---

## 3. 运行时

### 3.1 启动顺序

1. 壳取单实例互斥量 `Local\freev6-desktop-single-instance`；抢不到就退出。
2. 生成 32 字节 hex token（每次启动新生成），预留一个空闲 loopback 端口。
3. 启动 sidecar，注入环境变量：

   | 变量 | 含义 |
   |---|---|
   | `FREEV6_ROOT` | 安装目录（助手据此定位 `config/` `state/` `runtime/`） |
   | `FREEV6_API_TOKEN` | 本机 API 的 Bearer token |
   | `FREEV6_API_PORT` | 预留端口；失败时助手回落 13335 |
   | `FREEV6_PARENT_PID` | 父进程 PID；父消失则自行收尾（旧壳不传 → 关闭监视） |

4. 助手建 Job（kill-on-close）并自加入，再监听 loopback。
5. 前端加载后开始轮询：`/status` 3s、`/connections`（直连 9090，2.5s 超时）1s、
   `/proxies` 5s、内存 3s；用户点「启动」才真正起内核。

### 3.2 端口与鉴权

| 端口 | 用途 | 谁监听 |
|---|---|---|
| 随机高位（`FREEV6_API_PORT`） | 助手本地 API | helper |
| 13335 | 旧版/回落端口 | helper（未传 `FREEV6_API_PORT` 时） |
| 127.0.0.1:9090 | mihomo 控制口（`external-controller`） | `f6-core` |
| 127.0.0.1:7890 | 内核 mixed proxy（仅本机） | `f6-core` |
| 0.0.0.0:1053（tcp+udp） | 内核 DNS | `f6-core` |
| `FreeV6TUN` | TUN 适配器（无端口概念） | `f6-core` |

启动前 `mihomo.CheckListenPorts()` 会逐个试探 7890/9090/1053，占用即 409 `port-conflict`。

本地 API 守卫链：`withCORS(guardLocalOnly(requireAuth(handler)))`

- 无/错 token → 401；`Origin` 非本机或 `null` → 403；`Host` 非 loopback → 403；
  `OPTIONS` → 204；方法不符 → 405。这些是回归基线，改动后要重跑（见 §7.3）。

### 3.3 单实例、托盘、自启、提权

- **单实例**：Win32 命名互斥量 `Local\freev6-desktop-single-instance`（按登录会话隔离）；
  第二个实例弹「FreeV6 已在运行」后退出。
- **托盘**：左键显示窗口；右键菜单含退出（走 §5.3 的收尾）。
- **自启**：任务计划 `FreeV6 Autostart`（`schtasks`），由助手创建/删除，参数含
  `--autostart`（静默启动）。
- **提权**：生产清单 `requireAdministrator`；`FREEV6_TEST_MANIFEST=1` 时构建脚本换成
  `asInvoker`，让 `cargo test` 能在不提权时跑（见 §7.4）。

### 3.4 API 一览（全部前缀 `/api/v1`）

| 方法 | 路径 | 作用 | 前端超时 |
|---|---|---|---|
| GET | `/status` | 运行状态/流量/内存 | 10s |
| GET | `/runtime` | 内核版本与可用版本 | 10s |
| GET | `/runtime/check` | 检查内核更新 | 30s |
| GET/PUT | `/settings` | 读/写设置（部分字段可省略） | 10s |
| POST | `/settings/reset` | 恢复默认（能修损坏的 settings.json） | 10s |
| POST | `/proxy/start` | 启动免流 | **420s** |
| POST | `/proxy/stop` | 停止免流 | **200s** |
| POST | `/shutdown` | 收尾并退出（壳退出/更新用） | 6s（壳侧） |
| POST | `/warp/register` | 注册 WARP 设备 | 35s |
| GET | `/update/latest?channel=beta` | 检查应用更新 | 25s |
| PUT | `/update/download` | 下载安装包（同步校验入参） | 10s |
| GET | `/update/progress` | 下载进度 | 10s |
| GET | `/notice` | 公告（并写缓存） | 20s |
| POST | `/ipv6/probe` | IPv6 出口探测 | 10s |
| GET/PUT/DELETE | `/chain-proxy` | 链式代理 | 45s |
| GET/PUT/DELETE | `/cloud-config` | 备用配置 | 45s |
| GET | `/appearance` | 外观/背景 | 10s |

> 超时表在 `web/lib/api-timeouts.js`，规则：**≥ 服务端预算 ×1.5**（`/proxy/start`
> 例外，按「各阶段之和 + 10s」）。新增接口必须同时登记两边，见 §11.9。

---

## 4. 磁盘上的状态

`root` = `freev6-desktop.exe` 所在目录（本机示例 `D:\app\FreeV6`；默认 per-user 安装
是 `%LOCALAPPDATA%\FreeV6`）。壳通过 `FREEV6_ROOT` 传给助手。

| 路径 | 内容 | 敏感 | 删了会怎样 |
|---|---|---|---|
| `config\settings.json` | 全部开关与白名单 | 中 | 回落默认值（丢失自启/白名单设置） |
| `config\chain-proxy.yaml` | 链式代理导入内容 | **高**（机场凭据） | 链式开关置开时会报「内容缺失」 |
| `config\cloud-profile.yaml` | 备用配置节点 | **高** | 同上 |
| `state\warp.json` | WARP 设备凭据（私钥） | **极高** | 下次启动重新注册 |
| `state\mihomo.yaml` | 生成的内核配置，**含 MASQUE 私钥** | **极高** | 启动时重新生成 |
| `state\mihomo-controller.secret` | 9090 控制口密钥 | 高 | 重启后重新生成 |
| `state\mihomo.pid` / `.pid.meta` | 内核 PID 与归属 | 低 | 孤儿内核需手动结束 |
| `state\network-snapshot.json` | 启动前的网络基线（热点/ICS/DNS） | 中 | 停止时无法还原网络，下次启动自愈 |
| `state\notice-cache.json` | 公告缓存（含维护者开关结论） | 低 | 备用配置门禁 fail-closed（锁定）直到联网 |
| `state\core-manifest.json` | 内核清单缓存（6h TTL） | 低 | 下次联网重拉 |
| `state\helper.log` | 助手 stderr（壳转发写入） | 低 | 排障线索丢失 |
| `state\mihomo.log` | 内核日志 | 低 | 同上 |
| `state\cache.db` / `ruleset\` | 内核自身的缓存/规则 | 低 | 自动重建 |
| `runtime\cores\f6-core-*.exe` | 当前 + 上一个内核（回滚位） | 低 | 自动重新下载 |
| `%LOCALAPPDATA%\FreeV6\freev6-update-setup.exe` | 已下载的安装包 | 低 | 重新下载 |

> 备份/迁移：只拷 `config\settings.json` 就够「恢复习惯」；连 `config\chain-proxy.yaml`、
> `config\cloud-profile.yaml` 一起拷才是「恢复出口」。**不要**把 `state\` 整体拷到另一台
> 机器（含私钥与 PID）。

助手启动时还会做两件"静默清理"（不告警、失败下次再试）：删除旧版本遗留的地理数据
（约 27MB，已无引用）和不再被引用的规则集缓存文件。看到这些文件消失不是故障。

---

## 5. 关键流程

### 5.1 启动免流（`POST /proxy/start`）

顺序（全部在 `cmd/freev6-helper/main.go`）：

1. 解析 body、规范化模式/网段（锁外，避免慢请求占锁）。
2. `h.mu`（进程内代理互斥）→ `configMu` 短暂读 settings + 两个内容文件 → 释放 `configMu`。
3. 链式/备用内容解析、互斥检查；配置自检（`f6-core -t`）。
4. 残留快照自愈（≤120s）、DNS 静态污染修复（≤30s）、网络快照（≤10s）。
5. 起内核、等 `/version` 就绪（≤15s）、校验控制器密钥。
6. 热点共享：探测（30s）→ 切换（120s）→ 失败回滚（60s）；失败**不阻断**免流。
7. 返回 `{running:true, hotspot, controllerSecret, core, network}`。

> 最坏耗时 ≈ 400s（各阶段之和）。前端档位 420s 由此而来；把某一阶段预算调大时，
> 必须同步 `api-timeouts.js` 与 `tests/api-timeouts.test.js`。

### 5.2 停止（唯一顺序）

`internal/lifecycle.StopSession` 是 GUI / CLI / 退出共用的唯一实现：

```text
热点还原（锁外，≤120s，失败不阻断）
  → [会话锁] 关 TUN 并等适配器消失（≤4s）
  → 停内核并等退出（≤2s）→ 重读 PID
  → 清残留 TUN 路由（≤3s）→ 删快照
```

失败策略：还原失败仍停内核（快照保留）；停核失败**不清路由**（内核可能还在收尾）；
新会话接管则跳过清理。快照不存在不算异常（不报 `restoreSkipped`）。

### 5.3 退出与被强杀

- **正常退出**：壳 `mark_helper_stopping` → `POST /shutdown`（1.5s）→ 等 Terminated
  （≤3s）→ 强杀兜底。助手在 `/shutdown` 里跑 §5.2，然后 `Shutdown(5s)+Close+exit`。
- **壳被强杀**：助手有父进程句柄监视，父消失即跑 §5.2 再退出（实测 20–430ms 起步）。
  等代理互斥 `h.mu` 的预算 420s（覆盖一次长启动），拿到锁后收尾预算 150s。
- **助手被强杀**：助手持有 kill-on-close Job 并已自加入，内核与临时子进程一起死。
  **行为变化**：以前杀助手网络仍可用（留下无主提权内核）；现在会断，下次启动自愈。

### 5.4 应用更新

```text
前端 GET /update/latest?channel=beta
  → helper 并发拉 stable(releases/latest)/beta(beta-channel) 的 version.json + .sig
  → Ed25519 验签（失败 = 没有更新），验签通过的清单缓存 60s
  → 前端比较版本，弹窗
PUT /update/download {url, sha256}
  → 同步校验 URL 白名单 + sha256 格式（400 而不是先 200 后失败）
  → 逐条镜像下载（5 个 gh-proxy + 直连；停滞 10s 换线，单线硬上限 3 分钟）
  → 落盘 %LOCALAPPDATA%\FreeV6\freev6-update-setup.exe，边下边算 SHA
壳 update_apply
  → mark_helper_stopping → POST /shutdown
     ├ Accepted   ：等 Terminated（6s），超时中止
     ├ StoppedOnly：旧 helper（/shutdown 404）在 /proxy/stop 成功后被杀，继续
     └ Refused    ：复位 stopping，中止并提示手动安装
  → reap_stray_helpers(3s) → 确认无 helper 残留 → 启动 NSIS（/S /FREEV6REL）→ 退出
```

### 5.5 内核更新（`f6-core`）

- 清单：`evansrrr/f6-core` release 里的 `core.json`（6h 缓存；也是同一批镜像前缀）。
- 版本比较后下载到 `runtime/cores/f6-core-<os>-<arch>-v3-<version>.exe`，保留上一版为
  回滚位；`MarkVerified` 只标记**本次实际启动成功**的版本，`Prune` 只清理不再需要的。
- **未接入签名**（A2，见 §12）：内核清单/二进制目前只有 HTTPS + 哈希一致性，没有
  类似应用更新的验签。

---

## 6. 安全模型

1. **本地 API**：token 每次启动新生成、只经环境变量传递、只监听 loopback；Origin/Host
   双向校验挡 DNS rebinding；CORS 只放行本机来源。
2. **更新信任链**：`version.json` 与安装包同源（同一镜像族），哈希不能单独作锚点，所以
   清单必须带 Ed25519 签名：
   - 信封：`v1.<keyId>.<base64(签名)>`，签名覆盖 `version.json` 原始字节。
   - 公钥内置在二进制：`internal/appupdate/update_public.pem`（可多块，支持换钥）。
   - 失败关闭：无公钥/无 `.sig`/验签失败 → 当作「没有更新」，绝不回落未签名清单。
3. **发布侧**：`release.yml` 没配私钥就拒绝出包；上传后把资产拉回来再验签、并按清单
   里的 `sha256` 校验安装包。
4. **前端**：CSP `default-src 'self'`，无远程脚本；所有渲染走 `textContent`/转义。
5. **提权**：整个应用以管理员运行（TUN/ICS 需要）。因此本地 API 的守卫是安全边界，
   改动鉴权/守卫必须当作安全改动评审。

### 6.1 密钥运维

```powershell
# 生成（私钥只落文件，公钥打印到 stdout）
go run ./cmd/freev6-update-sign -gen-key -key-id 2026-11 -key-out .\update-signing-key.pem
# 公钥追加进 internal/appupdate/update_public.pem 并提交
# 私钥内容填进 GitHub Secret：FREEV6_UPDATE_SIGNING_KEY
# 离线备份后删除本地私钥
```

- 换钥：新公钥**追加**（不删旧的），发一版客户端，等覆盖后再换 Secret 与
  `env.UPDATE_KEY_ID`。
- 丢钥：只能让用户手动装一次带新公钥的版本。**私钥必须有离线备份**。
- 细节与故障处理：`docs/release.md`。

---

## 7. 构建、开发与测试

### 7.1 本地开发

```powershell
# 一次性
go install github.com/tauri-apps/tauri-cli@2.11.4   # 或 cargo install tauri-cli --version 2.11.4 --locked

# 开发（自动构建 sidecar）
cargo tauri dev

# 只改前端：直接跑一个静态服务器指向 web/ 也能调 UI，但 API 需要壳注入的 token
```

### 7.2 构建安装包

```powershell
cargo tauri build
# 产物：src-tauri/target/release/bundle/nsis/FreeV6_<base-version>_x64-setup.exe
# 注意：beta 的完整版本号只在 tag/index.html 里，NSIS 只用 base 版本（见 §8.3）
```

### 7.3 门禁命令（与 CI 等价）

| 目的 | 命令 |
|---|---|
| Go 测试 | `go test -count=1 ./...`（约 39s） |
| 竞态 | `go test -race ./internal/... ./cmd/freev6-helper`（**本机需 gcc**，见 §11.2） |
| 静态检查 | `go vet ./...`；`go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...` |
| 格式 | `gofmt -l cmd internal`（本机 CRLF 会误报，见 §11.1） |
| 前端语法 | `for f in web/*.js web/lib/*.js; do node --check "$f"; done` |
| 前端测试 | `node --test tests/*.test.js`（38 个） |
| 图标子集 | `python3 scripts/check-icons.py` |
| Rust | `cargo check --locked`；`$env:FREEV6_TEST_MANIFEST=1; cargo test --locked`（8 个用例） |

### 7.4 Rust 测试为什么要那个环境变量

生产清单是 `requireAdministrator`，测试二进制继承它 → 不提权运行会 `os error 740`。
`src-tauri/build.rs` 在 `FREEV6_TEST_MANIFEST=1` 时把清单换成 `asInvoker`，于是
`cargo test` 可以正常跑（8 个用例，0.6s）。**CI 目前只跑 `cargo check`**，把测试接进
CI 是批次 5 的一项（见 §12）。

### 7.5 真机调试技巧（不打扰正在运行的实例）

```bash
# 临时根 + 高位端口 + 隔离开机缓存目录
export FREEV6_ROOT="C:/Temp/fv6-root" FREEV6_API_PORT=38911 \
       FREEV6_API_TOKEN=<64 hex> LOCALAPPDATA="C:/Temp/fv6-cache"
./freev6-helper.exe
curl -H "Authorization: Bearer $FREEV6_API_TOKEN" http://127.0.0.1:38911/api/v1/status
```

- 启动耗时基准：`go run ./scripts/boot-measure -bin <helper.exe> -root <临时根> -n 8`
- 结束测试进程要按 PID `taskkill`（任务运行器会把主动结束记为 exit 1，属正常）。

---

## 8. 发布

> 完整清单在 `docs/release.md`；这里是决策要点。

### 8.1 版本号四处（少一处就构建失败）

| 文件 | 写什么 | 示例（beta） |
|---|---|---|
| `web/index.html` 的 `#versionValue` | **完整版本** | `FreeV6 v0.5.7-beta.2` |
| `src-tauri/tauri.conf.json` | 基础版本（数字） | `0.5.7` |
| `src-tauri/Cargo.toml` | 同上 | `0.5.7` |
| `src-tauri/Cargo.lock` | 同上 | `0.5.7` |
| `CHANGELOG.md` | `## [完整版本]` 小节 | `## [0.5.7-beta.2]` |

原因：NSIS 的 ProductVersion 只吃数字；客户端从 `#versionValue` 判断自己是不是测试版
（决定要不要查 beta 通道）。

### 8.2 流程

```powershell
git tag v0.5.8-beta.1 ; git push origin v0.5.8-beta.1
# → Release 工作流：构建 → 改名 → 签名 → 上传 → 回验 → 草稿
# → 人工点 Publish
# → beta 通道：release:published 事件 或 每天 4 次定时对账 或手动 dispatch
```

- tag 带 `-` 自动标为 Pre-release（`releases/latest` 会跳过它）。
- 发布后 `beta-channel` 指针才会切换；旧客户端能否发现新版本取决于指针。
- **只在用户明确要求时打 tag**（本项目的约定）。

### 8.3 安装包命名（踩过坑，务必读完）

Tauri 的 NSIS 只用**基础版本**命名，所以 beta 构建产出 `FreeV6_0.5.7_x64-setup.exe`，
与正式版同名。流水线在构建后统一改名：

| tag | release 资产 |
|---|---|
| `v0.5.7` | `FreeV6_0.5.7_x64-setup.exe`（不变） |
| `v0.5.7-beta.2` | `FreeV6_0.5.7-beta.2_x64-setup.exe` |

**不要手工在 release 页面改名**：签名清单里的 `setup`/`download` 已经签过，改资产名
只会让更新 404（v0.5.7-beta.1 就这么坏过）。要换名字就换 tag。

### 8.4 发布后验收（3 分钟）

```bash
curl -sL .../releases/download/beta-channel/version.json | head -c 200
go run ./cmd/freev6-update-sign -verify -in version.json -sig version.json.sig
# 资产名必须等于清单里的 setup；再用清单 download 拉一次，比对 sha256
```

工作流里的 `Verify the uploaded assets` 已经做了后两步，人工只需确认草稿里的资产名。

### 8.5 失败与回滚

- 构建失败：删 tag（`git push --delete origin <tag>` + `git tag -d`），修完重打。
- 发出去了但有问题：再发一版（更新链只升不降）；必要时把 `beta-channel` 指回旧版本
  （手动 dispatch 旧 tag），但**已安装的新版不会自动降级**。
- 签名私钥丢失：见 §6.1。

---

## 9. CI

| workflow | 触发 | 内容 |
|---|---|---|
| `ci.yml` `go` | push / PR | gofmt 检查、`go test`、`go vet`、staticcheck v0.8.2（windows） |
| `ci.yml` `go-race` | 同上 | `go test -race ./internal/... ./cmd/freev6-helper`（ubuntu） |
| `ci.yml` `rust` | 同上 | 版本一致性（三处）、构建 sidecar、`cargo check --locked` |
| `ci.yml` `web` | 同上 | 所有脚本 `node --check`、`node --test`、图标子集 |
| `release.yml` | 推 `v*` tag | 17 步：校验版本 → 构建 → 改名 → 清单 → 签名 → attestation → 上传 → 回验 |
| `beta-channel.yml` | release published / 定时 / 手动 | 验签 → 校验资产名存在 → 幂等比对 → 传 `.sig` 与清单 → 回验 |
| `docs-deploy.yml` | release published（跳过预发布） | 调 Vercel Deploy Hook 重建文档站 |

注意：

- `release: published` 事件**可能不触发**（2026-11-28 实测过一次），所以 beta 指针有
  定时对账兜底；文档站没有兜底，需要手动 dispatch。
- beta 通道在推指针前会校验「清单里的安装包名 == 目标 release 的资产名」，对不上就失败
  ——这是有意的闸，别为了让它变绿去放宽。

---

## 10. 运维与排障

| 症状 | 先看 | 常见原因 / 处理 |
|---|---|---|
| 打开就弹「组件异常」 | `<root>\state\helper.log` 末尾 20 行 | 端口被占（旧 helper 残留）、杀软隔离、sidecar 缺失；任务管理器结束所有 `freev6-helper.exe` 再开 |
| 启动一直转圈 | helper.log 的 `stop/start` 阶段行 | 热点切换失败会回滚（最长 ~200s）；观察是否出现 `hotspot:failed` |
| 停止很慢 | helper.log `helper: stop: tun teardown …` | 内核关 TUN 约 1s 是正常的；>4s 说明适配器没消失 |
| 停止后没网 | `state\network-snapshot.json` 是否还在 | 还原失败会保留快照；下次启动自愈。不要手动删快照 |
| 更新检查不到 | `/update/latest?channel=beta` 的 `error` 字段 | 验签失败（公钥/Secret 不匹配）、镜像全挂、离线 |
| 点更新下载 404 | 清单 `setup` 与 release 资产名 | 见 §8.3，禁止手工改名 |
| 更新提示"未在 6 秒内退出" | 是否有旧 helper 在跑 | 旧版 helper 不支持 `/shutdown`，会在 `/proxy/stop` 后被杀；若仍失败，手动退出重试 |
| 装了新版但组件还是旧的 | 安装前是否退出过应用 | NSIS 装前会检查；混合构建只能重装修复 |
| 备用配置提示"当前未开放" | `state\notice-cache.json` 的 `cloudProfile` | 维护者关了这个开关，或从未成功拉过公告（离线） |
| 内存/流量不刷新 | `:9090` 是否可达、内核是否在跑 | 前端 3s 超时后下一轮会重试 |

日志位置：`<root>\state\helper.log`、`<root>\state\mihomo.log`；壳的 stderr 在
`<root>\state\helper.log`（转发）与控制台（`cargo tauri dev` 时）。

---

## 11. 已知限制与陷阱

1. **本机 `gofmt -l` 会误报一片**：工作区可能是 CRLF，而 `gofmt` 只认 LF。判断格式要看
   LF 副本或用 CI 结果；`.gitattributes` 已设 `* text=auto eol=lf`，提交后仓库里是 LF。
2. **本机不能跑 `-race`**：没有 gcc（cgo 不可用）。CI 的 `go-race` job 覆盖。
3. **`cargo test` 需要 `FREEV6_TEST_MANIFEST=1`**：否则测试二进制带 requireAdministrator，
   不提权直接 740；CI 目前没接这一步。
4. **`go.mod` 的 module 路径是占位符** `github.com/yourname/freev6`：历史遗留，改它要全仓
   替换 import，收益低、风险高，暂不动。
5. **`test/` 目录是遗留**（一个 `1.log`），不参与任何流程。
6. **未入库的本地目录**：`bin/`、`build/`、`config/`、`runtime/`、`state/`、`test/`、
   `docs/agent/`、`.worktrees/` 都在 `.gitignore` 里（含本地笔记与调试产物）；`docs/` 下
   入库的是 `design/`、`release.md`、`handover.md`、`cloud-profile.md`、`notice.md`、
   `agreement.md`、`privacy.md`。
7. **应用未做代码签名**：SmartScreen 会拦，README 已写明「更多信息 → 仍要继续」。
8. **结束 helper 会断网**（Job 连带内核）：这是有意的所有权模型，写进了 CHANGELOG，
   发版说明要保留。
9. **超时表与预算耦合**：改服务端预算必须同步 `web/lib/api-timeouts.js` 与
   `tests/api-timeouts.test.js`；「相等即错」（客户端与预算相等必然先超时）。
10. **安装包命名**：见 §8.3，别手工改名。
11. **release 事件不可靠**：beta 指针有定时兜底，文档站没有。

---

## 12. 当前状态与路线图

改进计划总表：`docs/design/2026-11-28-improvement-plan-v2.md`（含每批状态与核实结论）。

| 批次 | 内容 | 状态 |
|---|---|---|
| 1 | 更新信任链（Ed25519 签名、并发拉清单、同步 400） | 完成，`0.5.7-beta.1` 起生效 |
| 1.1 | 重定向守卫、原始下载地址、上传后验安装包、beta 对账 | 完成 |
| 2 | 生命周期：Job、`/shutdown`、父监视、会话锁、`MarkVerified`、CLI 对齐 | 完成 |
| 2.1 | 安装包命名规则、`go-race` 修复、stopping 复位、快照缺失 | 完成，`0.5.7-beta.2` |
| 3 | 配置一致性、分层超时、节点列表韧性、公告门禁 | 完成 |
| 3.1 | clear/start 进配置事务、start 420s/stop 200s、父监视锁预算、`updatedAt` | 完成 |
| 4 | 前端 UMD 拆分 + `api()` 抽取与行为测试 + Rust 纯用例 lib 化 | **待做** |
| 5 | CI 门禁：Actions 固定 SHA、`cargo test` 接入、格式脚本、dependabot | **待做** |
| A2 | 内核清单/二进制签名（`f6-core` 仓库） | 待做，跨仓库 |

文档索引：

```text
docs/release.md                         发版清单 + 签名密钥运维
docs/handover.md                        本文件
docs/design/2026-11-28-improvement-plan-v2.md     总计划与批次状态（从这里开始）
docs/design/2026-11-28-baseline-test-record.md    改动前基线（性能对照）
docs/design/2026-11-28-batch{1,2,3}-plan.md       各批设计与复核
docs/design/2026-11-28-batch{1,2,3}-test-record.md 各批验证记录（含实测数字）
docs/cloud-profile.md                   备用配置的服务端约定
docs/notice.md                          公告 notice.json 的格式
docs/agreement.md / docs/privacy.md     用户协议与隐私政策（打包进安装包）
```

遗留的小项：Dependabot 有 1 条 moderate 告警未处理（`github.com/evansrrr/free-v6/security/dependabot`）。

---

## 13. 交接清单

### 13.1 必须转移的凭据与权限

| 项 | 在哪 | 交接方式 |
|---|---|---|
| GitHub 仓库写权限 | `evansrrr/free-v6` | 加 collaborator / 转移仓库 |
| 更新签名私钥 | **不在仓库** | 离线备份（密码管理器/加密盘）当面移交；同时更新 Secret |
| Actions Secret `FREEV6_UPDATE_SIGNING_KEY` | 仓库 Settings → Secrets | 接手人可重设（需私钥全文） |
| Actions Secret `DOCS_DEPLOY_HOOK` | 同上 | 由 Vercel 项目重建后替换 |
| `f6-core` 仓库（内核） | `evansrrr/f6-core` | 内核发布流程独立，需一并移交 |
| 文档站 / 官网 / 图床 | README 里的 `freev6.cc.cd`、`imgs.ich.cc.cd` | **需原维护者确认归属与续费** |

### 13.2 建议问清楚的问题

1. `f6-core` 的内核清单由谁发布？`core.json` 的字段约定有没有文档？
2. 备用配置的 Worker（按源 IP 段下发）部署在哪？`docs/cloud-profile.md` 是否仍准确？
3. 公告 `notice.json` 的发布流程（直接改 main 还是走 PR）？`cloudProfile` 开关谁有权改？
4. 官网/文档站/Vercel 项目的账号归属与计费。
5. 是否有未合并的分支或本地实验（`.worktrees/` 下有历史工作树，未纳入流程）。

### 13.3 接手第一周建议

1. 按 §0 跑一遍门禁，确认本机环境（尤其 Rust/WebView2/NSIS）。
2. 用 §7.5 的方式起一个隔离 helper，走一遍 `/status → /proxy/start → /proxy/stop`。
3. 读 `docs/design/2026-11-28-improvement-plan-v2.md` 的 §2（复核结论）与 §5（预算表）。
4. 做一次不改代码的发布演练：只 bump 版本 + 改 CHANGELOG，打一个 beta tag，看 17 步
   是否全绿、草稿资产名是否等于清单 `setup`。
5. 再动代码。优先级建议：批次 5 的 `cargo test` 接入（1 行）+ 批次 4 的前端拆分。

---

## 14. 术语

| 词 | 含义 |
|---|---|
| 免流 | 只让不计流量的 IPv6 目标走隧道，其余走直连 |
| 链式代理 / chain | 用户导入的订阅，作为隧道的上游出口 |
| 备用配置 / cloud profile | 维护者下发的节点集，与链式代理互斥 |
| 回滚位 | `CoreStore` 保留的上一版内核，启动失败时可回退 |
| 混合构建 | 壳与 helper 版本不一致（更新中断的后果），会让 API 端口对不上 |
| beta 指针 | `beta-channel` release 里的 `version.json`，测试版客户端唯一发现入口 |
| 会话锁 | 跨进程互斥量 `Local\FreeV6-MihomoStart`，串行化启动/停止 |
| 代理互斥 `h.mu` | 进程内锁，串行化 start/stop/shutdown（start 全程持有） |
| Job | Win32 Job Object，kill-on-close，保证助手死了内核也死 |
