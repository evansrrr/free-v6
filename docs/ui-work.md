# freev6 前端 UI 重设计方案

## 设计定位

freev6 是一个校园网 IPv6 代理工具，面向技术用户。设计方向：**工具感 × 视觉精致** — 像 Linear、Raycast 那样的现代开发者工具，不是营销落地页。

核心原则：
- **一个记忆点**：流量图表是视觉英雄，其余保持安静
- **无 emoji**：所有图标替换为 1.5px 描边 SVG，统一视觉语言
- **信息即结构**：不加无意义装饰，每个视觉元素传递信息
- **克制动画**：只在状态变化时动，不做入场秀

## Design Read

```yaml
artifact: desktop proxy dashboard (Tauri 2)
audience: 技术用户，校园网环境
visual-language: Modern Tool SaaS (Linear / Raycast school)
mode: overhaul (保留逻辑，重写视觉)
visual-variance: 4/10 (工具型，不需要花哨变体)
motion-intensity: 2/10 (状态反馈为主)
information-density: 6/10 (紧凑但不拥挤)
asset-dependence: 1/10 (纯 SVG + CSS，无外部图片)
brand-fidelity: 3/10 (无既有品牌规范)
```

## 设计系统

### 色板

保持现有 Material You dark tokens，微调使色调更统一冷峻：

| Token | 值 | 用途 |
|---|---|---|
| surface | `#121318` | 页面底色 |
| surface-container | `#1c1d24` | 卡片背景 |
| surface-container-high | `#272830` | hover 态、输入框 |
| primary | `#7eaaff` | 强调色（稍调暖，更柔和） |
| primary-container | `#1a3a6b` | 选中态背景 |
| on-surface | `#e2e3ea` | 主文字 |
| on-surface-variant | `#8b8d98` | 次要文字 |
| outline-variant | `#3a3c45` | 边框、分割线 |
| green | `#5cd89a` | 连接成功、低延迟 |
| orange | `#f0a050` | 中等延迟 |
| red | `#ff6b6b` | 高延迟 / 错误 |

### 字体

- **显示 / 标题**：Manrope 700/800 — 几何无衬线，有力量感
- **正文 / UI**：DM Sans 400/500/600 — 温和易读
- **数据 / 代码**：JetBrains Mono 400 — 等宽，用于 IP、速率、延迟数字

### 间距

基础单位 4px，常用倍数：8, 12, 16, 20, 24, 32

### 圆角

| 层级 | 值 | 用途 |
|---|---|---|
| xs | 6px | 小按钮、tag |
| sm | 10px | 输入框、节点卡片 |
| md | 14px | 卡片 |
| lg | 20px | 导航栏、FAB |
| full | 9999px | 药丸形 |

### 阴影

仅用于 FAB 和浮层，卡片无阴影（靠背景色区分层级）：
- FAB：`0 4px 16px rgba(0,0,0,.4)`
- 抽屉：`-8px 0 24px rgba(0,0,0,.3)`

## SVG 图标系统

所有 emoji 替换为 16×16 / 20×20 内联 SVG，stroke-width 1.5，stroke-linecap round，stroke-linejoin round，fill none。颜色继承 currentColor。

### 图标清单

| 位置 | 旧 emoji | 新 SVG 描述 |
|---|---|---|
| 导航-仪表盘 | 📊 | 折线图：底部横线 + 一条上升折线 |
| 导航-代理 | 🌐 | 圆球 + 经纬线（简化3条弧线） |
| 导航-设置 | ⚙️ | 齿轮（6齿，内圆） |
| 导航-日志 | 📋 | 文档 + 3条横线 |
| 卡头-流量 | 📈 | 上升箭头 + 小波浪线 |
| 卡头-统计 | 📊 | 饼图（270° 弧 + 分割线） |
| 刷新按钮 | ↻ | 圆弧箭头（顺时针） |
| FAB-测试 | ⚡ | 闪电（简化折线） |
| FAB-启停 | ▶ / ■ | 三角 / 方块 |
| 设置-外观 | 无 | 半圆 + 太阳光线 |
| 设置-代理 | 无 | 双向箭头（左右） |
| 设置-核心 | 无 | 芯片引脚（矩形+4线） |
| 设置-关于 | 无 | 圆圈 + i |
| 设置-下载 | 无 | 向下箭头 + 横线 |
| 设置-注册 | 无 | 钥匙（圆形+齿） |
| 空态 | 🌐 | 云 + 断线 |
| 搜索 | 无 | 放大镜 |
| 返回 | ← | 左箭头 |
| 关闭 | ✕ | × 号 |
| 展开 | › | 右 chevron |

## 布局优化

### 导航栏

- 宽度 64px 不变
- 导航项：SVG 图标 20×20 + 文字 10px，间距 2px
- 选中态：primary-container 背景 + primary 色图标，无边框
- hover：surface-container-high 背景

### 仪表盘

两张卡片纵向堆叠，间距 10px：

1. **流量图卡片**
   - 卡头：左侧 SVG 图标 + 标题，右侧 ↑↓ 速率（JetBrains Mono）
   - 图表区：高度 140px，深色底 surface-container-low
   - 上传线：primary 色，2px，底部半透明渐变填充
   - 下载线：`#7eaaff` 的 60% 透明度变体，1.5px

2. **统计卡片**
   - 左侧：SVG 双环图（外环上传 primary，内环下载 tertiary）
   - 右侧：上传/下载累计值（JetBrains Mono）
   - 环图动画：stroke-dashoffset 600ms ease-out

### 代理页

节点网格，`repeat(auto-fill, minmax(160px, 1fr))`，间距 8px：

- 卡片：surface-container 背景，sm 圆角，12px 内边距
- 选中态：primary 边框 2px + primary-container 背景
- 延迟色彩：<100ms green / <500ms orange / ≥500ms red
- 测试中：spinner 动画 + 文字"测试中"
- 右下角 FAB：闪电 SVG + "测试延迟"

### 设置页

分组列表，max-width 560px：

- 分组标题：12px，primary 色，大写追踪
- 列表项：SVG 图标 + 标签 + 描述 + 右侧控件
- 点击态：surface-container-high 背景

### 启停 FAB

- 位置：右下角 20px
- 尺寸：56×56px 圆形
- 停止态：surface-container-high 背景 + on-surface-variant 图标
- 运行态：green-container 背景 + green 图标 + 3px green glow
- 动画：350ms emphasized ease

### 日志抽屉

- 宽度 320px
- 无阴影，靠 1px outline-variant 边框分隔
- 条目：时间 JetBrains Mono primary 色 + 消息 on-surface-variant

## 动画规范

| 场景 | 时长 | 缓动 |
|---|---|---|
| 页面切换 exit | 120ms | ease-out |
| 页面切换 enter | 250ms | ease-out |
| FAB 状态变化 | 350ms | cubic-bezier(0.2,0,0,1) |
| 卡片 hover | 150ms | ease |
| 抽屉开合 | 280ms | cubic-bezier(0.2,0,0,1) |
| 环形图填充 | 600ms | ease-out |
| spinner 旋转 | 600ms | linear (infinite) |

## 技术约束

- 原生 HTML / CSS / JS，无框架
- Tauri 2 WebView（Chromium）
- Go helper at 127.0.0.1:13335
- 窗口默认 900×600，最小 720×480
- 所有颜色通过 CSS 变量
- JetBrains Mono 通过 Google Fonts CDN 加载