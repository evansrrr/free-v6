# freev6

Windows 10/11 上的 IPv6-only WARP/MASQUE 实验客户端。项目目标是把 Cloudflare WARP 设备注册、MASQUE 密钥 enroll、mihomo 配置生成和本地进程控制拆成可测试的核心模块，之后再接入 Tauri GUI。

## 当前阶段

已建立第一阶段核心骨架：

- Go CLI 作为稳定的核心入口
- Cloudflare WARP 注册和 MASQUE enroll 客户端
- P-256 私钥转换为 mihomo 所需的 SEC1 Base64
- mihomo MASQUE 节点 YAML 生成器
- 可注入 HTTP 客户端的 API 测试边界

尚未实现：Windows TUN/路由接管、mihomo 进程生命周期、DPAPI 凭据保护和 Tauri GUI。

## 开发

需要 Go 1.25+ 和 Git。

```powershell
go test ./...
go run ./cmd/freev6 version
go run ./cmd/freev6 render-config -state state/warp.json -out state/mihomo.yaml
```

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

## 设计原则

1. 核心逻辑不依赖 GUI，可以单独测试和排障。
2. 所有外部网络请求都有超时，并可替换 HTTP client。
3. 设备状态、订阅配置和运行时日志分离。
4. 网络切换必须可恢复，关闭功能时先停止 mihomo，再恢复系统网络状态。

## 许可证

许可证将在项目完成第一版发布前确定，并与 mihomo 等依赖的许可证义务一起审核。
