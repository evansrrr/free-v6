# 公告发布指南

仪表盘的「公告」卡片读取仓库根目录的 [`notice.json`](../notice.json)。发公告不需要发版、不需要 token、不需要动代码：

1. 打开 GitHub 网页（手机上也可以）→ `notice.json` → 铅笔图标编辑
2. 往 `items` 数组**开头**加一条公告
3. 顺便把 `updated` 改成当前时间
4. 提交到 `main`（Commit changes）

客户端每次启动检查一次，点右上角刷新也会重查。镜像与浏览器缓存合计大约 5 分钟内生效。

## 字段说明

```json
{
  "id": "2026-10-30-maintenance",
  "title": "10/30 凌晨 2:00-4:00 骨干维护",
  "body": "维护期间免流可能频繁断连。\n受影响：全部接入点。",
  "level": "warning",
  "published": "2026-10-29T20:00:00+08:00",
  "expires": "2026-10-30T06:00:00+08:00",
  "link": "https://freev6.cc.cd/guide/introduction",
  "linkText": "查看使用说明"
}
```

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `id` | 是 | 唯一标识，改文案后保持不变才算「同一条公告」；未读标记靠它判断。建议 `日期-简短英文` |
| `title` | 是 | 标题，≤ 200 字；卡片折叠态只显示这一行（两行截断） |
| `body` | 否 | 正文，纯文本，`\n` 换行，≤ 4000 字；不支持 Markdown |
| `level` | 否 | `info`（默认）/ `warning` / `critical`，决定卡片图标的配色 |
| `published` | 是 | 发布时间，ISO 8601 带时区，如 `2026-10-29T20:00:00+08:00` |
| `expires` | 否 | 过期时间；到点后客户端不再显示这一条（不必回来删） |
| `link` | 否 | 详情链接，**只接受 https**；非 https 会被丢弃 |
| `linkText` | 否 | 链接按钮文案，默认「查看详情」 |

## 约定与限制

- 最多 20 条，按你写的顺序展示（新的放前面）；卡片折叠态显示第一条。
- 单条格式错（缺 `id`/`title`/`published`、时间解析不了、`link` 非 https）只会丢掉这一条，不会让整个公告板变空。
- 「重要的公告长期置顶」= 把 `id` 保持不变、把 `title`/`body` 改成新内容，并更新 `published`。
- 公告下线最快的办法是直接删掉这条 item；也可以加 `expires` 让它自动消失。
- 接口失败时（离线、镜像全挂）会回退到本机上次成功拉取的缓存（最多 7 天），卡片上会标注「离线缓存」。
- 这个文件只放公告，不要放密钥或个人信息——它会被所有客户端拉取。

## 排障

想看客户端拿到的到底是什么：自己起一个 helper（token 用你指定的值，端口 13335 空闲时可用）

```bash
FREEV6_API_TOKEN=dev ./freev6-helper.exe
curl -H "Authorization: Bearer dev" http://127.0.0.1:13335/api/v1/notice
```

`stale: true` 表示这次用的是本机缓存；`error` 字段会写明所有镜像的失败原因。公告没更新先确认 `raw` 路径可访问：`https://github.com/evansrrr/free-v6/raw/main/notice.json`。
