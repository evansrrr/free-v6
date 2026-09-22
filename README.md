# freev6

Windows 10/11 上的 IPv6-only WARP/MASQUE 实验客户端。项目包含 Go 核心、Material You Tauri GUI 和 mihomo helper sidecar；特权网络动作仍通过后端 API 收口。

## 开发

需要 Go 1.25+ 和 Git。

```powershell
go test ./...
go run ./cmd/freev6 version
go run ./cmd/freev6 render-config -state state/warp.json -out state/mihomo.yaml
go run ./cmd/freev6 start -state state/warp.json -campus-cidr 10.0.0.0/8,172.16.0.0/12
go run ./cmd/freev6 status
go run ./cmd/freev6 stop
```

默认生成 `tun + rule` 配置，所有非本地流量经过 WARP；只有通过 `-campus-cidr` 明确添加的校园网段和局域网规则允许 `DIRECT`。校园网网段应按实际认证网关、门户、DNS 和内网服务填写，不能把公网网段加入绕过列表。

需要全局模式时使用 `-mode global`。它仍然不提供外部 `DIRECT` 出口，只是让 mihomo 的 `GLOBAL` 组接管所有连接。节点选择仍在 `🚀 节点选择` 组内完成，可选择自动测速、故障转移或单个节点。

Windows TUN 会创建虚拟网卡并修改路由/DNS，`start` 会先检查管理员权限。项目已加入 Windows 网卡、默认网关和 DNS 快照采集基础；关闭 mihomo 前仍需要完成快照持久化、路由/DNS 恢复和异常退出清理。

真实注册会创建新的 WARP 设备并写入本地状态，请先确认符合 Cloudflare 服务条款及所在网络的使用规定：

```powershell
go run ./cmd/freev6 register -name freev6-windows -state state/warp.json
```

注册状态包含敏感凭据，只应保存在本机，不要提交到 GitHub 或上传为公共订阅。

## 目录

```text
cmd/freev6/       CLI
internal/warp/    WARP API、密钥和设备状态
internal/mihomo/  mihomo MASQUE 配置生成
internal/app/     后续承载进程和 Windows 生命周期
src-tauri/        后续 GUI 外壳预留
```

MVP 运行链路是：先执行 `register` 保存设备状态，再执行 `start` 自动发现并启动安装目录 `runtime/` 或开发目录 `state/` 中的 mihomo Alpha。启动会生成带回环控制端点的配置，检查代理组并验证 IPv6 出口；同时支持 `-binary` 显式覆盖、`-mode rule|global`、`-campus-cidr` 和 `-egress-url` 参数。

## 设计原则

1. 核心逻辑不依赖 GUI，可以单独测试和排障。
2. 所有外部网络请求都有超时，并可替换 HTTP client。
3. 设备状态、订阅配置和运行时日志分离。
4. 网络切换必须可恢复，关闭功能时先停止 mihomo，再恢复系统网络状态。

## 许可证

许可证将在项目完成第一版发布前确定，并与 mihomo 等依赖的许可证义务一起审核。
