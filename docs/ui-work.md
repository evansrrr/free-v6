# freev6 前端 UI/UX 规划

## 技术约束

- 原生 HTML / CSS / JavaScript，不引入框架
- Tauri 2 WebView（Chromium 内核）
- 后端：Go helper at `127.0.0.1:13335`，所有数据经 REST API
- 保持现有 CSS 变量体系（Material You），扩展而非替换
- 保持现有字体 DM Sans + Manrope
- 单文件 HTML 结构（`index.html`），分离 `styles.css` + `app.js`

---

## 设计方向

### 视觉语言

- **Material You / Material 3**：动态主色、语义色 token、圆角卡片
- **FlClash 风格**：浅色主题优先，简洁控制中心，节点网格卡片
- **低视觉噪音**：大量留白、无边框分割、柔和阴影、克制用色
- 不使用赛博朋克、霓虹渐变、玻璃拟态、Bootstrap 风格

### 主题

- 支持**深色**、**浅色**（默认）、**跟随系统**
- 深色主题参考 FlClash 截图：深灰背景 `#1e1e2e`，卡片 `#2a2a3c`，强调色用 Material You primary
- 所有颜色通过 CSS 变量管理，绝不硬编码色值
- 主色（primary）支持用户自选或跟随系统 accent color

---

## 导航架构

### 左侧导航栏（Rail）

```
┌──────────┐
│  [f6]    │   ← brand mark
│          │
│  🏠 仪表盘│   ← data-view="dashboard"
│  🌐 代理  │   ← data-view="proxies"
│  ⚙ 设置  │   ← data-view="settings"
│          │
│  ≡ 日志  │   ← drawer toggle（底部）
└──────────┘
```

- 宽度 72px（图标+文字），窄窗口 56px（仅图标）
- 当前页高亮：图标背景变为 primary-container 色
- 日志按钮在底部，点击打开右侧抽屉
- 导航切换动画：280ms ease，fade + slide-up

### 页面切换

- 使用与现有相同的 `[data-view]` 机制
- 每个页面是一个 `<section class="view">`，通过 `.active` 控制显隐
- 切换时旧页面 fade-out（150ms），新页面 fade-in（280ms）

---

## 页面 1：仪表盘（Dashboard）

### 页面标题区

```
仪表盘                          [● 已连接] [✏]
```

- 左侧：页面标题 "仪表盘"
- 右侧：连接状态 chip（与 header 同步）+ 编辑按钮（预留）

### 卡片布局：FlClash 风格网格

```
┌──────────────────────────┐  ┌─────────────────┐
│  📈 实时流量             │  │  🔀 免流模式    │
│  ↑ 0 B/s   ↓ 0 B/s      │  │                 │
│  [═════════canvas═══════] │  │  [━━━━━━━]━━━━  │
│  最近60秒流量折线图       │  │  ON / OFF       │
│                          │  │                 │
│                          │  └─────────────────┘
├──────────────────────────┤  ┌─────────────────┐
│  🏷 出站模式              │  │  🌐 网络检测    │
│  ● 规则   ○ 全局         │  │  104.28.240.149 │
│                          │  ├─────────────────┤
│                          │  │  📡 内网 IP     │
│                          │  │  26.243.245.156 │
└──────────────────────────┘  └─────────────────┘
                            ┌─────────────────┐
                            │  📊 流量统计     │
                            │  [环形图] ↑23.3KB│
                            │          ↓39 KB  │
                            └─────────────────┘
```

**响应式**：窄窗口时变为单列堆叠。

### 卡片详细规格

#### 1. 实时流量卡片

- 标题行：图标 + "实时流量" + 右侧 ↑↓ 速率数字
- 主体：60 秒滚动折线图（Canvas 绘制）
  - 上传线：primary 色，下载线：secondary 色
  - 底部填充半透明渐变
  - X 轴 60 格（每格1秒），Y 轴自适应
  - 无网格线，仅折线 + 填充
- 无数据时显示 "等待流量数据" 空态

**数据来源**：mihomo controller `GET /connections`，取 `upload`/`deltaUpload`、`download`/`deltaDownload`
- 前端每秒轮询一次，存储最近 60 个采样点
- 计算 delta（当前值 - 上一次值）= 实时速率
- 流量统计取累计值

#### 2. 免流模式开关卡片

- FlClash 风格的大号滑动开关
- 中间显示 ON / OFF 文字
- 开关下方：当前模式小字（"规则模式" / "全局模式"）
- 点击触发 `/proxy/start` 或 `/proxy/stop`
- 过渡动画：开关滑动 + 背景色从 muted → primary

#### 3. 出站模式卡片

- Radio group 样式：规则 / 全局（与现有 segmented 一致）
- 选中项显示 Filled radio + primary 色
- 切换时立即调用 `/settings` PUT 保存
- 下次启停免流时生效

#### 4. 网络检测 + 内网 IP 卡片

- 上半：国旗 emoji + "网络检测" + info icon
  - 显示当前出口 IP
- 下半：分割线 + 内网 IP
  - 显示本机 IPv4 / IPv6 地址
- 数据来源：`/status` API 中的网络信息

#### 5. 流量统计卡片

- 环形图（SVG circle + stroke-dashoffset）
  - 上传色：primary
  - 下载色：tertiary
- 右侧图例 + 累计数字
  - ↑ 上传：XX KB / MB / GB（自动单位）
  - ↓ 下载：XX KB / MB / GB
- 数据来源：同实时流量卡片的累计值

---

## 页面 2：代理（Proxies）

### 页面标题区

```
代理                    🔍  ⏺  ⋮
```

- 左侧标题 "代理"
- 右侧：搜索 icon、延迟测试 icon、更多菜单

### 代理组 Tab 栏

```
🚀 节点选择  ☑️ 手动切换  ♻️ 自动选择  🔄 故障转移  🛑 全球拦截  🐟 漏网之鱼
```

- 水平滚动 tab 栏（可滚动不换行）
- 当前组高亮：底部 primary 色指示线
- 点击切换显示该组下的节点列表

### 节点网格

FlClash 风格的卡片网格布局：

```
┌───────────┐ ┌───────────┐ ┌───────────┐ ┌───────────┐
│ WARP6-103 │ │ WARP6-103 │ │ WARP6-104 │ │ WARP6-103 │
│ 103-2-500 │ │ 103-2-4500│ │ 104-2-500 │ │ 103-2-443 │
│           │ │           │ │           │ │           │
│ Masque    │ │ Masque    │ │ Masque    │ │ Masque    │
│ 39 ms     │ │ 39 ms     │ │ 39 ms     │ │ 40 ms     │
└───────────┘ └───────────┘ └───────────┘ └───────────┘
```

**卡片规格**：
- 圆角14px，背景 surface-container
- 第一行：节点名称（加粗，截断省略）
- 第二行：节点类型（"Masque"，灰色小字）
- 第三行：延迟数字 + 单位（绿色 <100ms，橙色 <500ms，红色 "Timeout"）
- 当前选中节点：primary 色边框（2px）+ 浅 primary 背景
- 点击卡片 = 手动切换到该节点

**网格**：`grid-template-columns: repeat(auto-fill, minmax(160px, 1fr))`

### 延迟测试

- 右下角 FAB（浮动操作按钮）：⚡ 延迟测试
- 点击后对当前组所有节点发起 `GET /proxies/:name/delay?timeout=5000`
- 测试中：卡片延迟数字显示为 spinner
- 测试完成：更新各节点延迟值
- FlClash 风格：测试期间 FAB 变为 loading 状态

### 搜索

- 点击搜索 icon 展开搜索栏（顶部滑入动画）
- 实时过滤节点名称
- 支持按延迟排序（下拉菜单或右键）

### 数据来源

- 代理组和节点列表：`GET /proxies` → `proxies` 对象
- 当前选中：每个 group 的 `now` 字段
- 延迟：`GET /proxies/:name/delay`
- 切换节点：`PUT /proxies/:name` body `{"name": "节点名"}`

---

## 页面 3：设置（Settings）

### 页面标题区

```
设置
```

### 设置分组

采用 FlClash 风格的 list-item 布局：每项 = 图标 + 标题 + 描述 + 右侧控件

```
┌─────────────────────────────────────────────────────┐
│ 🎨 外观                                            │
│ ├─ 主题          深色/浅色/跟随系统        [Select]  │
│ ├─ 强调色        蓝/紫/绿/橙/红           [Select]  │
├─────────────────────────────────────────────────────┤
│ ⚙ 应用                                            │
│ ├─ 开机自启      随系统自动启动             [Switch] │
│ ├─ 最小化到托盘  关闭窗口时最小化           [Switch] │
│ ├─ 检查更新      启动时检查新版本           [Switch] │
├─────────────────────────────────────────────────────┤
│ 🌐 代理                                            │
│ ├─ 工作模式      规则/全局                 [Segment]│
│ ├─ 校园网段      管理绕过 CIDR             [→]      │
│ ├─ DNS 策略      当前: Cloudflare + 阿里   [→]      │
├─────────────────────────────────────────────────────┤
│ 🔧 核心                                            │
│ ├─ mihomo 版本   Alpha v1.xx.xx            [pill]   │
│ ├─ 下载核心      自动下载最新版            [Button] │
│ ├─ 注册 WARP     设备注册状态              [Button] │
├─────────────────────────────────────────────────────┤
│ ℹ️ 关于                                             │
│ ├─ 版本          freev6 v0.1.0                      │
│ ├─ 许可证        MIT License                        │
│ ├─ GitHub        打开项目主页              [→]      │
└─────────────────────────────────────────────────────┘
```

### 校园网段管理页

点击 "校园网段" 后展开/导航到子页面：

```
← 返回        校园网段

[输入 CIDR 例如 10.0.0.0/8    ] [添加]

┌──────────────────────┐
│ 10.0.0.0/8      [×] │
│ 172.16.0.0/12   [×] │
│ 192.168.0.0/16  [×] │
└──────────────────────┘

提示: 只填写校园网私有或认证网段，不要加入公网地址。
```

---

## 日志抽屉

保持现有侧边抽屉设计，微调：

- 标题行：诊断 + "运行日志"
- 日志条目：时间戳（primary 色）+ 消息
- 自动滚动到最新
- 最大 100 条，自动清理旧条目
- 错误日志高亮（error-container 色背景）

---

## 通用组件规范

### CSS 变量（扩展现有体系）

```css
/* 深色主题（默认） */
:root {
  --md-primary: #a0c4ff;
  --md-on-primary: #003258;
  --md-primary-container: #1d4e89;
  --md-on-primary-container: #d3e4ff;
  --md-secondary: #bac8db;
  --md-on-secondary: #263141;
  --md-secondary-container: #3c4858;
  --md-on-secondary-container: #d7e3f8;
  --md-tertiary: #d0bcff;
  --md-on-tertiary: #381e72;
  --md-surface: #1e1e2e;
  --md-surface-container: #2a2a3c;
  --md-surface-low: #242436;
  --md-on-surface: #e4e1e9;
  --md-on-surface-variant: #a8a4b0;
  --md-outline: #6e6a78;
  --md-outline-variant: #44415a;
  --green: #50d890;
  --green-container: #1a3d2c;
  --orange: #f0a050;
  --orange-container: #3d2a10;
  --error: #ffb4ab;
  --error-container: #93000a;
}
```

### 卡片

```css
.surface {
  background: var(--md-surface-container);
  border: 1px solid var(--md-outline-variant);
  border-radius: 16px;
  box-shadow: 0 1px 3px rgba(0,0,0,.2);
}
```

### 按钮层级

| 用途 | 样式 | 示例 |
|------|------|------|
| 主操作 | Filled Button | 启动免流、延迟测试 |
| 次要操作 | Tonal Button | 切换模式、下载核心 |
| 辅助操作 | Text Button | "全部设置 →" |
| 危险操作 | Error Filled | 停止免流（开启状态）|
| FAB | Filled + 圆形 | 右下角延迟测试 |

### Switch 控件

- 使用原生 `<input type="checkbox">` 自定义样式
- 轨道：48×28px，圆角14px
- 滑块：20px 圆形
- 关闭态：surface-variant 色
- 开启态：primary 色 + 滑块 primary-on

### Select / Segmented

- Select：下拉菜单，圆角12px
- Segmented：与现有一致，pill 形状，3px gap

### 状态指示

| 状态 | 颜色 | 图标/样式 |
|------|------|-----------|
| 已连接 | green | ● + "已连接" chip |
| 未连接 | outline | ● + "未连接" chip |
| 加载中 | primary | spinner |
| 错误 | error | ! + 红色背景 |
| 超时 | orange | "Timeout" 文字 |

---

## 响应式断点

| 宽度 | 布局 |
|------|------|
| ≥1024px | 左导航栏（72px）+ 主内容区 |
| 620-1023px | 左导航栏缩窄（56px，仅图标）+ 主内容区 |
| <620px | 底部 tab 栏（4 个 tab），隐藏左侧栏 |

---

## 数据流架构

### 状态管理

```javascript
const state = {
  // 连接状态
  helperOnline: false,
  proxyRunning: false,

  // 仪表盘
  mode: 'rule',           // rule | global
  egressIP: '',           // 当前出口 IP
  localIP: '',            // 内网 IP
  trafficUpload: 0,       // 累计上传 bytes
  trafficDownload: 0,     // 累计下载 bytes
  trafficHistory: [],     // 最近 60 秒 [{t, up, down}]

  // 代理
  proxyGroups: {},        // { groupName: { now, all, type } }
  activeGroup: '',        // 当前查看的代理组
  selectedProxy: '',      // 当前选中节点

  // 设置
  settings: { mode: 'rule', campusCidrs: [] },

  // 核心
  runtime: { present: false, version: '' },

  // UI
  activeView: 'dashboard',
  theme: 'dark',          // dark | light | system
  logs: [],
};
```

### API 轮询策略

| 数据 | 轮询间隔 | 说明 |
|------|----------|------|
| 状态 `/status` | 3 秒 | helper 在线、admin、warp、proxy 运行 |
| 代理组 `/proxies` | 5 秒 | 代理列表 + 当前选中 + 延迟 |
| 流量 `/connections` | 1 秒 | 实时流量折线图数据 |
| 核心 `/runtime` | 仅启动时 | 运行时状态 |

### 错误处理

- API 请求失败：显示 Snackbar（底部3秒自动消失）
- helper 离线：全页 disconnected overlay
- 启动失败：Snackbar 显示具体错误 + 提供操作建议

---

## Helper API 扩展

当前 helper 需新增以下端点支持新 UI：

```
GET  /api/v1/proxies                 → 代理组 + 节点列表 + 当前选中
GET  /api/v1/proxies/:group/delay    → 批量延迟测试
PUT  /api/v1/proxies/:group          → 切换节点 {"name": "..."}
GET  /api/v1/connections             → mihomo 实时流量
GET  /api/v1/network                 → 本机 IP + 出口 IP
```

这些端点透传 mihomo controller（`127.0.0.1:9090`）的 `/proxies`、`/connections`、`/group` 等 API。

---

## 实现分期

### Phase 1：基础框架 + 仪表盘（核心）

重写 `index.html` / `styles.css` / `app.js`：
- 新导航栏（3 页 + 日志抽屉）
- 仪表盘5张卡片
- Canvas 流量图
- 深色主题 + CSS 变量体系
- 免流开关 + 模式切换

### Phase 2：代理页

- 代理组 tab 栏
- 节点网格卡片
- 延迟测试 FAB
- 节点切换交互

### Phase 3：设置页

- 分组设置列表
- 主题切换（深色/浅色/系统）
- 强调色选择
- 校园网段管理子页
- 核心/关于信息

### Phase 4：打磨

- 响应式适配（底部 nav、窄屏）
- 过渡动画优化
- 无障碍（aria-label、keyboard nav）
- 空态 / 加载态 / 错误态完善
