# Changelog

<!--
发布流程（配合 .github/workflows/release.yml）：
1. 递增 src-tauri/tauri.conf.json 与 src-tauri/Cargo.toml 的 version；
2. 把 [Unreleased] 的内容整理成本次版本的 `## [x.y.z] - YYYY-MM-DD` 小节；
3. 打 tag 并推送：
     git tag vX.Y.Z
     git push origin vX.Y.Z
   （带 -beta / -alpha 后缀的 tag 会自动标记为 Pre-release）
工作流按 tag 提取对应小节作为 GitHub Release 描述：
先匹配完整版本（如 0.3.0-beta.1），再匹配基础版本（0.3.0）；
两者都没有 → 构建失败，提醒先补本文件。
Release 永远先建为草稿（draft），检查无误后手动 Publish。
-->

## [Unreleased]

### Added
- 启动时后台检查 GitHub 正式版 release（`releases/latest`，排除 beta/alpha），有新版弹窗提示；“跳过此版本”记住选择，设置页版本行仍显示“有新版本”角标、点击可再次打开
- 软件内自动更新：下载进度、SHA256 校验、更新时自动停止兔流、静默安装完成后免 UAC 自动重启（无需手动下载运行 setup.exe）
- 更新检查与安装包下载前部走 `api.gitproxy.dev` 代理加速大陆访问；检查改读 release 内的 `version.json` 稳定地址（`releases/latest/download`，仅命中正式版、不占 api.github.com 配额），代理失败自动回退直连，检查由 helper 代理获取（无 CORS 问题）

## [0.2.8] - 2026-09-26

### Added
- 开机自启动改用任务计划程序（ONLOGON + 最高权限）：登录后静默启动、不再弹 UAC；任务管理器“启动应用”中可查看、禁用与重新启用
- “静默启动”开关（默认关闭）：开启后开机自启与手动打开均不弹窗口，仅驻留托盘
- 退出前确认弹窗：免流运行中通过托盘退出会先提示（取消 / 停止并退出）
- 升级安装器在 freev6 运行时弹出“请先退出软件”提醒（NSIS PREINSTALL 钩子），点击重试直至进程退出

### Fixed
- 开机自启会弹出主窗口（tauri.conf.json 重复 `visible` 键，后者覆盖前者）
- 13335 端口被上次异常退出残留的 helper 占用导致启动失败（新 helper 自动接管孤儿进程；活跃实例不受影响）

## [0.2.3] - 2026-09-26

### Added
- 自绘无边框标题栏（拖拽区 + 窗口按钮），关闭按钮隐藏到托盘
- 托盘图标左键单击直接打开主窗口，右键菜单保留
- 启动/停止免流按钮改用自绘 SVG 图标（圆角播放三角 / 圆角方块）
- 开发者模式：直连模式选项、已注册时显示“重新注册”
- 启动免流前 IPv6 校验（非校园网弹窗提示，开发者模式跳过）
- 动态取色（壁纸主色/系统强调色）与纯黑背景开关（默认关闭）
- 初版开机自启动（托盘驻留）
