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
