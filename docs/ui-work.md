# freev6 前端 UI 方案 — Material Web

> 本方案以 Material 3 / **Material Web**（`@material/web` 2.5.0）为唯一设计语言。
> 组件用法参考 [`material-web-2.5.0/docs/components/`](../../material-web-2.5.0/docs/components/)，
> 主题与 token 参考 [`docs/theming/`](../../material-web-2.5.0/docs/theming/)，
> 架构参考 [`docs/intro.md`](../../material-web-2.5.0/docs/intro.md) 与 [`docs/quick-start.md`](../../material-web-2.5.0/docs/quick-start.md)。

## 设计定位

freev6 是校园网 IPv6 代理工具，面向技术用户。方向：**工具感 × Material 精致** —— 像 Material Web 官方 catalog 那样的组件化产品界面，不是营销页。

核心原则（Material Web 版）：
- **组件优先**：凡是 Material Web 提供的控件，一律使用 `md-*` 组件，不自绘、不仿制
- **一个记忆点**：流量图表是视觉英雄，其余保持安静
- **Token 唯一真源**：颜色 / 排版 / 形状只有 token，没有散落的硬编码皮肤
- **结构与皮肤分离**：app CSS 只负责布局与自绘结构，皮肤全部归组件 shadow DOM
- **克制动画**：只在状态变化时动，遵循 M3 easing token

```yaml
artifact: desktop proxy dashboard (Tauri 2, static web + import map)
audience: 技术用户，校园网环境
visual-language: Material 3 / Material Web (custom dark-blue scheme)
mode: component-driven (md-* 优先，自绘仅限无组件场景)
visual-variance: 3/10 (深/浅/跟随系统 三态，无花哨变体)
motion-intensity: 2/10 (状态反馈为主)
information-density: 6/10 (紧凑但不拥挤)
asset-dependence: 2/10 (Material Symbols Outlined 图标字体 + Google Fonts；无图片)
```

## 技术架构

- Tauri 2 `frontendDist: "../web"`，**纯静态、无打包、无编译**
- `index.html` 头部 `import map` 把 `@material/web/` 与 `lit*`、`tslib` 指到 `web/vendor/`（依赖闭包按需拷贝，见 `web/vendor/README.md`）
- `web/material.js`（module）统一注册用到的 `md-*` 元素
- 主题经 `data-theme="dark|light|system"` 切换；组件跟随 `--md-sys-color-*`，`color-scheme` 同步原生控件

## 设计系统

### 色彩与主题（[theming/color.md](../../material-web-2.5.0/docs/theming/color.md)）

M3 color scheme 全角色映射到 freev6 色板，**只在 `:root` 维护一套值**，
`--md-sys-color-*` 通过 `var(--md-*)` 桥接（深浅切换自动跟随，组件与 app 同源）：

| M3 角色 | 深色值 | 用途 |
|---|---|---|
| primary | `#7eaaff` | 强调、下载曲线、聚焦环 |
| primary-container | `#1a3a6b` | 选中态、FAB 常态 |
| surface | `#121318` | 页面底色 |
| surface-container | `#1c1d24` | 卡片、分组 |
| surface-container-high | `#272830` | hover、输入区 |
| on-surface / on-surface-variant | `#e2e3ea` / `#8b8d98` | 主 / 次文字 |
| outline / outline-variant | `#7a7d88` / `#3a3c45` | 边框、分隔 |
| error（= `--red`） | `#ff6b6b` | 错误、超时延迟 |

语义辅助色 `--green`（成功 / 低延迟）、`--orange`（中延迟）不在 M3 角色表内，保留为 app 层变量。
浅色板在 `[data-theme="light"]` 与 `@media (prefers-color-scheme: light) [data-theme="system"]` 各一份 ——
CSS 无法跨选择器复用声明，**修改浅色值必须同步两处**（已知接受项）。

### 排版（[theming/typography.md](../../material-web-2.5.0/docs/theming/typography.md)）

- 组件字族：`--md-ref-typeface-plain: 'DM Sans'`、`--md-ref-typeface-brand: 'Manrope'`（组件 typescale 自动继承）
- 页面层级：页标题 20/700 Manrope ≈ title-large；分组标题 11/700 + 0.06em 追踪 ≈ label-small（大写）
- 数据：JetBrains Mono 仅用于速率 / 累计 / 延迟 / 日志时间（原生等宽例外，不进 token）

### 形状（[theming/shape.md](../../material-web-2.5.0/docs/theming/shape.md)）

| app token | 值 | 用途 |
|---|---|---|
| --radius-xs | 6px | 图标底板、pill |
| --radius-sm | 10px | 节点卡片、品牌标 |
| --radius-md | 14px | 卡片、分组 |
| --radius-lg | 20px | 导航项 |
| --radius-full | 9999px | 药丸（chip、FAB 由组件内置 shape token） |

### 间距

4dp 网格：常用 8 / 12 / 14 / 16 / 24。卡片间距 10，设置组间距 12，节点网格 gap 14。

### 阵列（[components/elevation.md](../../material-web-2.5.0/docs/components/elevation.md)）

- 卡片 / 分组**无阴影**，靠 surface 层级区分（M3 surface-container 阶梯）
- FAB 阴影由组件 `--md-fab-container-elevation` 内置
- 抽屉保留自定义 `--shadow-drawer`（右侧浮层，组件库无 drawer）

## 组件清单（[`docs/components/`](../../material-web-2.5.0/docs/components/)）

| 界面位置 | 组件 | 文档 | 关键约定 |
|---|---|---|---|
| 启停 / 延迟测试 FAB | `md-fab`（extended） | [fab.md](../../material-web-2.5.0/docs/components/fab.md) | `label` 属性改文案；`slot="icon"` 内为 `md-icon`（`play_arrow`/`stop`/`bolt`）；`.visible` 控显隐；`.running` / `.loading` 只覆写 token |
| 刷新 / 关闭 / 返回 / 删除 | `md-icon-button` | [icon-button.md](../../material-web-2.5.0/docs/components/icon-button.md) | 默认槽位放 `md-icon` |
| 下载 / 注册 / 添加 | `md-filled-button` | [button.md](../../material-web-2.5.0/docs/components/button.md) | `disabled` 属性；文本为默认槽内容；设置页主操作用 Filled 变体 |
| 主题选择 | `md-outlined-select` + `md-select-option` | [select.md](../../material-web-2.5.0/docs/components/select.md) | **选项文本写成元素内容**（非 label 属性）；监听 `change` 取 `e.target.value`；`.setting-md-select` 上 `--md-outlined-field-bottom-space:0` 压到 40px 行高 |
| 工作模式（单选） | `md-filter-chip` ×2 | [chip.md](../../material-web-2.5.0/docs/components/chip.md) | 点击后由 state 重同步 `.selected`；不 `preventDefault` |
| CIDR 输入 | `md-outlined-text-field` | [text-field.md](../../material-web-2.5.0/docs/components/text-field.md) | `value` 属性；Enter 触发添加 |
| 分组分隔线 | `md-divider` | [divider.md](../../material-web-2.5.0/docs/components/divider.md) | 替代 `.setting-item` 的 border-top；装饰性默认无 ARIA |
| 全部图标 | `md-icon` | [icon.md](../../material-web-2.5.0/docs/components/icon.md) | Material Symbols Outlined 连字文本；尺寸一律走 `--md-icon-size` token |
| 加载指示 | `md-circular-progress` | [progress.md](../../material-web-2.5.0/docs/components/progress.md) | `indeterminate`；`--md-circular-progress-size` 定尺寸（FAB 24px / 节点 14px） |

**自绘例外**（Material Web 无对应组件，按 M3 规范自绘）：
导航 rail（M3 navigation rail，无组件）、连接状态 chip、安装状态 pill（镜像 Filled button 的尺寸与排版：32px 高、radius-full、0×16 padding、12/500，颜色用 primary/绿/橙语义）、流量卡 + canvas 图表、统计环 SVG、节点卡片网格、日志抽屉、空态。

## 布局规范

- **导航 rail**：宽 80px（≤1024px 收 58px 隐藏文字），项间距 12，选中态 primary-container 药丸
- **顶栏**：标题 + 状态 chip + `md-icon-button`
- **视图切换**：enter 250ms decelerate（translateY 8→0）/ exit 120ms
- **仪表盘**：流量卡 + 统计卡纵向堆叠，gap 10，图表区 140px；统计卡 `width:fit-content` 贴合内容收窄；实时流量色带互换：上传 = secondary、下载 = primary（统计环保持 primary/tertiary 不变）
- **代理页**：`minmax(140px, 1fr)` 网格，gap 14；卡片 padding 10×12、min-height 58；FAB 距右下 24px（≤620px 移到 64/12）
- **设置页**：分组 = surface-container 圆角 14；**行高统一 56px**（`min-height:56` + padding 10×16，单行结构：图标 + 标签 + 右侧控件/值，无描述行）；行内控件 32–36px（select 36：`--md-outlined-field-top/bottom-space:6` + `--md-outlined-select-text-field-input-text-size:13` 对齐行标签字号；核心组 filled button 32：`--md-filled-button-container-height:32` + `label-text-size:12` + `leading/trailing-space:16`；pill `min-height:32` 同步镜像）；行间 `md-divider`；分组间 gap 12；关于页的版本/许可证值用 `.setting-value` 右置
- **日志抽屉**：宽 320px，fixed 右侧，scrim `rgba(0,0,0,.4)`

## 图标系统（`md-icon` + Material Symbols）

- **唯一图标来源**：`md-icon` 组件 + Material Symbols **Outlined** 连字文本，
  字体由 Google Fonts 加载（`family=Material+Symbols+Outlined`，与正文字体同一 CDN）
- **禁止**回退到内联 stroke SVG / emoji / 其他图标库（统计环是数据图形，不是图标，保留 SVG）
- **尺寸**：只用 `--md-icon-size`（同时决定字号与宽高）：nav 20 / 卡头与设置图标 16 / 箭头 14 / 空态 40 / FAB 24 / 图标按钮默认 24
- **颜色**：`currentColor` 继承，不单独设 fill
- **动态图标**：改连字文本即可（如启停 FAB 切换 `play_arrow` ↔ `stop`），不手绘 path
- 图标清单（位置 → 连字）：

  | 位置 | 连字 | 位置 | 连字 |
  |---|---|---|---|
  | 导航-仪表盘 | `monitoring` | 设置-外观 | `light_mode` |
  | 导航-代理 | `public` | 设置-工作模式 | `swap_horiz` |
  | 导航-设置 | `settings` | 设置-校园网段 | `lan` |
  | 导航-日志 | `receipt_long` | 设置-核心 | `memory` |
  | 顶栏-刷新 | `refresh` | 设置-下载 | `download` |
  | 卡头-流量 | `trending_up` | 设置-注册 | `key` |
  | 卡头-统计 | `donut_small` | 关于-版本 / 许可证 | `info` / `description` |
  | 速率-↑ / ↓ | `arrow_upward` / `arrow_downward` | CIDR-返回 / 删除 | `arrow_back` / `close` |
  | 空态 | `cloud_off` | 日志-关闭 | `close` |
  | FAB-启停 | `play_arrow` / `stop` | FAB-延迟 | `bolt` |
  | 列表-展开箭头 | `chevron_right` |  |  |

- 降级：字体不可达时连字文本会被 `overflow:hidden` 裁切（图标位留空），文字标签与 aria-label 仍在，不影响操作

## 状态与交互

| 场景 | 表现 |
|---|---|
| 延迟 | <100ms `--green` / <500ms `--orange` / ≥500ms `--red`（5000 显示 Timeout） |
| 测试中 | 节点行内 `md-circular-progress` 14px + “测试中” |
| FAB 运行态 | `.running`：`--md-fab-container-color: --green-container`、label/icon `--green` |
| FAB 加载态 | `.loading`（禁点 + 透明度），`#fabIcon` hidden ↔ `#fabProgress` 显隐，`label` 显示进度 `测试中 n/m` |
| 模式 chip | 单选互斥；重复点击已选项保持选中（从 state 重同步） |
| 选中节点卡 | primary 边框 + primary-container 底 |
| 空态 | 引导文案“请先启动免流模式…” |

## 动画与运动（M3 easing）

| token | 值 | 用途 |
|---|---|---|
| --ease-standard | cubic-bezier(.2,0,0,1) | hover、常规 |
| --ease-decelerate | cubic-bezier(0,0,0,1) | 视图进入、CIDR 子页 |
| --ease-emphasized | cubic-bezier(.2,0,0,1) | FAB、抽屉 280ms |

统计环 600ms ease-out；组件内部 motion 全部走组件自带 token，不覆盖。

## CSS 所有权与冲突规避 ★

这是“统一 Material Web 后不产生样式冲突”的硬规则：

1. **app CSS 只写**：布局壳（shell/rail/topbar/view/grid）、自绘结构（卡片、chip、抽屉、空态）、token 定义、reset
2. **对 `md-*` 只允许三类覆写**：
   - token 级：`--md-sys-color-*`、`--md-fab-*`、`--md-circular-progress-*` 等（含 `.fab-item` 上的主题覆写）
   - 宿主定位类：`.fab-item` 的 fixed 定位与 `.visible` 显隐
   - 槽位内容尺寸：如 `md-icon` 的 `--md-icon-size`、`md-fab` 图标位
3. **禁止**：选择器穿透 shadow、覆盖组件内部 class、再写自制按钮 / 下拉 / spinner / 分隔线（已全部由 md-* 取代）
4. 全局 reset 的 margin/padding 清零必须用 `:where(*):not(:is(md-* 列表))` **排除所有 md-* 宿主**——外层文档声明会压过组件的 `:host` 规则（与特异度无关，cascade layer 也无效），否则组件宿主内边距被清零（md-outlined-button 曾因此塌成裸文字）；新增 md-* 组件时必须同步加入该列表。原生 `button` 等规则只作用于 light-DOM 原生元素（组件内部在 shadow，天然隔离）；`[hidden]{display:none!important}` 用于组件槽位内 `hidden` 切换
5. **已清理的重复 / 死代码**（本方案落地时移除）：`--md-tertiary-container` 重复行、`--shadow-fab`（×3，FAB 改由组件 elevation）、`--ease-standard` 未用、`.mono`、原生 `input` reset（已无 light-DOM input）、`.node-type`（不再渲染）、`.brand-mark span`、`.setting-item` border-top（→ `md-divider`）、各处内联 SVG 尺寸规则（→ `--md-icon-size`）

## 响应式

- ≤1024px：rail 收 58px 隐藏文字，内容 padding 16
- ≤620px：rail 变底部栏（高 52），FAB 上移 64/12，节点网格 min 130px

## 资源与文档索引

- 组件与主题：`material-web-2.5.0/docs/`（intro、quick-start、theming/{color,shape,typography}、components/*）
- 供应链：`web/vendor/README.md`（闭包清单、许可证、再生成流程）
- 注册入口：`web/material.js`；import map：`web/index.html`

## 约束

- 无框架、无打包、无编译步骤；改动即所见
- Go helper `127.0.0.1:13335`；mihomo external controller `127.0.0.1:9090`
- 窗口默认 900×600，最小 720×480；Tauri CSP 为 null
- Google Fonts CDN：DM Sans / Manrope / JetBrains Mono / **Material Symbols Outlined**（图标字体）
- 应用 / 托盘图标：与 brand-mark 同源（Manrope 800 “F 6”、primary-container `#1a3a6b`、27.78% 圆角），产物 `src-tauri/icons/`（`icon.png` + 多尺寸 `icon.ico`，像素级断言验证），经 `bundle.icon` 同时供窗口与托盘（tray 显式取 `default_window_icon`）。**母图由 GDI+ 直接绘制**（`gen-icons.ps1` + `fix-font.ps1`：Manrope 可变字体实例化 wght=800）；勿用浏览器截图——小数 DPR（如 1.5）会把画布垫大、内容截断，产生“只显示左上角四分之一 + 白底”的废图标