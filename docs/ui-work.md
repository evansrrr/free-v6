# freev6 前端 UI/UX 规划

## 设计方向

缩减、简化、美化。减少视觉噪音，每个页面只保留核心功能。

## 技术约束

- 原生 HTML / CSS / JS，无框架
- Tauri 2 WebView（Chromium）
- Go helper at 127.0.0.1:13335
- CSS 变量管理所有颜色（Material You dark theme）
- 字体：DM Sans + Manrope

## 窗口尺寸

- 默认：900x600
- 最小：720x480
- 左侧导航栏 64px

## 页面结构

### 仪表盘（Dashboard）

只保留两张卡片，纵向堆叠：

1. **实时流量** - 60秒 Canvas 折线图 + 速率显示
2. **流量统计** - SVG 环形图 + 上传/下载累计

不包含：模式选择、网络检测、启停开关（已移至全局 FAB）。

### 代理（Proxies）

只显示自动选择组的节点网格：

- 不显示其他代理组 Tab
- 不显示搜索栏
- 不显示排序按钮
- 延迟测试完成后自动按延迟升序排列
- 点击节点卡片手动切换

右下角 FAB：延迟测试

### 设置（Settings）

分组列表：

- 外观：主题选择
- 代理：工作模式（规则/全局）、校园网段（子页面）
- 核心：mihomo 版本、下载核心、注册 WARP
- 关于：版本、许可证

## 全局元素

- **启停 FAB**：右下角固定，运行态绿色，停止态蓝色
- **连接状态**：右上角 chip，指示 mihomo 核心在线状态
- **日志抽屉**：右侧滑入，340px 宽

## 动画

- 页面切换：fade-out 150ms + fade-in 280ms
- 卡片 hover：translateY(-2px) + shadow
- FAB 状态：350ms emphasized cubic-bezier
- 日志抽屉：300ms slide-in + scrim fade