# 发版检查清单

版本号在仓库里有四处，语义不同，**任何一处不同步都会在发版时失败**（守卫在
`.github/workflows/release.yml`，编译前执行；CI 里另有一条不带 tag 的只读校验）。

## 发版前

- [ ] `git tag` 用完整版本：正式版 `v0.5.7`，测试版 `v0.5.6-beta.2`
- [ ] `src-tauri/tauri.conf.json` → `version` 写**基础版本**（`0.5.6`，不带 `-beta.N`）
- [ ] `src-tauri/Cargo.toml` → `version` 同上
- [ ] `web/index.html` → `#versionValue` 写**完整版本**（`FreeV6 v0.5.6-beta.2`）
- [ ] `CHANGELOG.md` 有 `## [完整版本]` 小节（工作流按 tag 提取，缺失即构建失败）
- [ ] 内核兼容区间同步：`f6-core/compat.json` 的 `minApp` / `maxApp` 覆盖本版客户端
- [ ] 本地跑一遍 CI 的两条校验：`go test ./...`、`cargo check --locked`

## 为什么基础版本和完整版本要分开写

`tauri.conf.json` 的版本会进 NSIS 安装包的 ProductVersion，那里只吃数字，
预发布后缀会被裁掉。所以预发布只能存在于 tag、`index.html` 和 CHANGELOG 三处。
客户端从 `#versionValue` 读自己的版本并据此决定更新通道——带 `-beta` 就请求
beta 通道，不带就只认正式版。这段字符串写错，正式版会永远自称测试版。

## 发布流程

1. 提交并推送上述改动
2. 打 tag 并推送：`git tag v0.5.6-beta.2 && git push origin v0.5.6-beta.2`
   - tag 里带 `-` 的一律标为 Pre-release（GitHub 的 `releases/latest` 会跳过它，
     正式版用户因此不会被推上测试版）
3. 工作流产出草稿，手动点“发布”
4. 发布测试版后，`beta channel` 工作流会把它的 `version.json` 复制到滚动指针
   release `beta-channel`，测试版客户端从那里发现新测试版
   - 若这一步失败或漏跑：`gh workflow run beta-channel.yml -f tag=v0.5.6-beta.2`

## 签名密钥运维（更新信任链）

`version.json` 必须带签名发布，否则新版客户端会拒绝这份清单（失败关闭）。
签名覆盖**上传的那份字节**，任何行尾/编码改动都会让验签失败。

### 一次性配置

1. 生成密钥对（私钥只落本地文件，公钥打印到 stdout）：
   ```powershell
   go run ./cmd/freev6-update-sign -gen-key -key-id 2026-11 -key-out update-signing-key.pem
   ```
2. 把 stdout 的 `-----BEGIN PUBLIC KEY-----` 块追加到
   `internal/appupdate/update_public.pem` 末尾并提交。**没有公钥时流水线会在
   "回验"步骤失败**，不会产出未签名版本。
3. 把 `update-signing-key.pem` 的**内容**填进仓库 Secret
   `FREEV6_UPDATE_SIGNING_KEY`（Settings → Secrets and variables → Actions）。
4. 做一份离线备份（密码管理器或加密盘），然后删除本地的
   `update-signing-key.pem`。私钥只有这一份，丢了就再也签不出能被现有客户端
   接受的清单。
5. `release.yml` 顶部的 `env.UPDATE_KEY_ID` 与实际 key id 保持一致（默认
   `2026-11`）。省略也可以：公钥指纹会被当作 id，两种 id 客户端都认。

Windows 上 `-key-out` 的 `0600` 只映射到只读属性，私钥的实际保护来自用户目录
ACL；共享机器上生成后请立即移入 Secret 并删除。

### 日常发布

工作流自动完成：签名 → 本机回验 → 先传 `.sig` 再传 `version.json` →
从 release 拉回来再验一次。任何一步失败都会让构建失败。

`beta-channel` 同步同样是"先 `.sig` 后清单"：这样即使中途失败，客户端看到的
也只是"新签名 + 旧清单"（验签失败 → 当作没有更新），而不是"新清单 + 旧签名"
被误当成有效。

### 换钥

1. 生成新密钥对，把**新公钥追加**到 `update_public.pem`（不要删旧的），提交并
   发一版客户端。这一步之后，新旧两把公钥都在客户端里。
2. 等这一版覆盖到用户之后，再把手里的私钥换成新的（Secret 与
   `env.UPDATE_KEY_ID` 一起改）。
3. 旧公钥可以永久保留：它只能验证旧签名，删掉反而会让用户停在无法升级的旧版。

### 丢钥 / 应急

私钥丢失时无法再产出被现有客户端接受的清单，只能：
1. 生成新密钥对，把新公钥加进 `update_public.pem`；
2. 用户必须**手动下载安装**一次带新公钥的版本；此后自动更新恢复。

这就是为什么公钥文件支持多块，以及为什么私钥必须有离线备份。

## 通道规则

| 客户端 | 能收到 |
| --- | --- |
| 正式版（版本串无 `-beta`/`-alpha`） | 只有正式版 |
| 测试版 | 测试版 + 正式版，取较新者 |

## 已知限制

- 安装包元数据（Windows“应用和功能”、exe 属性）显示基础版本，关于页显示完整
  版本。这是 NSIS 的 ProductVersion 限制，不是 bug。
- 早于 v0.5.6-beta.2 的测试版客户端无法识别预发布号，收不到后续测试版更新，
  需要手动下载一次。
