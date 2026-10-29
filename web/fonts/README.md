# web/fonts — 本地字体说明

应用的所有字体都在本目录本地内置，**不请求任何字体 CDN**（离线、免流未开、
开机冷启动时排版与图标都正常）。共四份 woff2，合计约 130KB：

| 文件 | 内容 | 体积 |
|---|---|---|
| `material-symbols-outlined.woff2` | Material Symbols 图标，**仅 45 个在用字形** 的连字子集（rlig） | ~6KB |
| `dm-sans-latin.woff2` | DM Sans 可变字体 latin 区段（wght 100–1000） | ~61KB |
| `manrope-latin.woff2` | Manrope 可变字体 latin 区段（wght 200–800） | ~24KB |
| `jetbrains-mono-latin.woff2` | JetBrains Mono 可变字体 latin 区段（wght 100–800） | ~39KB |

`@font-face` 声明在 `styles.css` 顶部；许可：图标 Apache-2.0（Google LLC），
三套正文字体 SIL OFL 1.1（见安装包 `LICENSES/`）。

## 将来加了页面文字，需要重新子集吗？

**不需要。** 三套正文字体是按 **latin unicode-range** 整段引入的可变字体：
任何新增拉丁文案、数字、域名、订阅节点名、日志文本都在覆盖范围内；
中文/emoji 本来就走系统字体（逐字符回退），与这三份文件无关。
字重同理——可变轴覆盖各族全部常用字重，新增用到的字重也无需动字体。

## 将来加了新图标，需要重新子集吗？

**需要**（图标子集只包含生成时写入的 45 个字形）。流程：

1. 在 `web/index.html` / `web/app.js` 里正常使用新的 `<md-icon>新图标名</md-icon>`；
2. 把新图标名追加进 `scripts/subset-icons.py` 顶部的 `ICON_LIGATURES` 列表；
3. 准备一份 Material Symbols Outlined 全量 woff2（Google css2 带现代
   User-Agent 与 `&text=` 参数可取，或 google/material-design-icons 仓库
   的 variablefont 目录）；
4. 运行：
   ```powershell
   pip install fonttools brotli   # 首次
   python scripts/subset-icons.py <全量输入.woff2> web\fonts\material-symbols-outlined.woff2
   ```
5. 重新打包。脚本会校验 45/45（更新后的数量）连字规则全部命中，缺了会报错退出。

不跑子集的后果：新图标名会以字母碎片形式显示（即 2026-10 那次线上问题的
样子），所以**加图标必须同步重跑子集**。

这一条现在有自动校验：`python scripts/check-icons.py` 扫描 index.html 与
app.js 里所有字体图标名，对不上子集就退出码 1（CI 的 web job 会跑它）。
忘了改清单也能在合并前拦住，不用等用户看到字母。

例外：少数图标（背景模式 / 更换壁纸 / 液态玻璃）改为**内联 SVG**，
不走字体——它们曾因未加入子集而显示成字母碎片（R / A / 工）。
内联 SVG 的约束：
- 根节点 `fill` 会被 Material 的 `::slotted(svg)` 覆盖成 currentColor，
  必须在 `.setting-icon md-icon svg` 里显式 `fill: none`，否则线稿变实心块；
- 尺寸用 `--md-icon-size`（settings 里是 16px），24 视框、2.2 描边，
  与 Material Symbols 在 16px 下的字重接近。
