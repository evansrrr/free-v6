<div align="center">
<img alt="logo" height="100" width="100" src="src-tauri/icons/128x128.png" />
<h2> FreeV6 </h2>
<p> 教育网通用 IPv6 免流客户端 </p>

[实现原理](https://ich.cc.cd/2026/05/11/mianliu/) | [Github](https://github.com/evansrrr/free-v6) | [用户协议](docs/agreement.md) | [隐私政策](docs/privacy.md)

<br />

<a href="https://github.com/evansrrr/free-v6/stargazers">
<img alt="Stargazers" src="https://img.shields.io/github/stars/evansrrr/free-v6?style=for-badge&logo=github&color=f4dbd6&logoColor=D9E0EE&labelColor=302D41"></a>
<a href="https://github.com/evansrrr/free-v6/releases/latest">
<img alt="Releases" src="https://img.shields.io/github/release/evansrrr/free-v6.svg?style=for-badge&logo=semantic-release&color=f5bde6&logoColor=D9E0EE&labelColor=302D41"/></a>
<a href="https://github.com/evansrrr/free-v6/blob/main/LICENSE">
<img alt="License" src="https://img.shields.io/github/license/evansrrr/free-v6?style=for-badge&logo=conventionalcommits&color=ee99a0&logoColor=D9E0EE&labelColor=302D41"></a>
<a href="https://github.com/evansrrr/free-v6/issues">
<img alt="Issues" src="https://img.shields.io/github/issues/evansrrr/free-v6?style=for-badge&color=a6da95"></a>
</div>

<br />

> [!CAUTION]
>
> # 本项目正在进行前期开发
>
> 软件并未正式对外发布，下载、使用、传播在此期间的项目代码或软件所引起的后果均与本项目无关

## 说明

支持以下桌面平台：

- Windows 10/11 x64（macOS 与 ARM64 在计划中，尚未发布）

在 [Releases](https://github.com/evansrrr/free-v6/releases/latest) 下载安装包。由于软件未签名，下载或首次运行如被拦截可选择“仍要继续”。软件安装与启动会请求管理员权限，如有系统提示，请选择同意。

运行软件后会在系统托盘显示图标，关闭窗口会缩小到托盘，左键单击托盘图标可重新打开界面。

*注：退出软件或开关机时会尽量恢复网络状态，但仍建议手动关闭免流，避免下次开机后校园网认证异常。*

任何免流方式都是功能大于体验

## FAQ

1. **我的校园网可以实现免流吗？**

   - 已接入 IPv4 的教育网会员单位，可按 IPv4 带宽 1:1 免费获得 IPv6 接入带宽，注意由于**高校校园网计费策略不同，务必确认您所在高校校园网可提供有效 IPv6 地址并支持 IPv6 流量免计费**。免流原理总结参考 [ich.cc.cd](https://ich.cc.cd/2026/05/11/mianliu/)

2. 启动免流失败

   - 确认已下载运行核心（`设置 → 核心状态`），未下载可遵循引导完成下载安装
   - 必要时可以点击“重新注册”重试
   - 确认本机 IPv6 正常（[testipv6](https://www.testipv6.cn/)），其他代理工具可能造成干扰，请关闭重试
   - 原则上本软件免流功能只对“第二代中国教育和科研计算机网”用户提供，请连接校园网使用

3. 启动免流后某些网站和软件打不开（如校内资源）

   - 请在 `设置 → 绕过校园网/白名单` 中添加校内网段或白名单域名，并重新启动免流

4. 软件没有响应 / 无法启动

   - 本地接口固定在端口 `13335`，若被其它软件或系统进程占用会导致启动失败

5. 反馈 Bug & 建议

   - 欢迎提 [issue](https://github.com/evansrrr/free-v6/issues)

## 开发

需要 Go 1.25+、Rust stable 和平台对应的 Tauri 系统依赖。Windows 还需要 Visual Studio C++ Build Tools。

## TODO

- [ ] 更换字体
- [ ] ip 信息
- [x] 流量统计
- [x] 自动运行
- [x] 快捷键
- [ ] 重置设置
- [ ] 崩溃清理与残留进程恢复
- [ ] 本地 API 鉴权
- [ ] macOS 与 ARM64 支持

## 免责声明

本软件仅供学习和研究使用，请勿用于任何非法用途。使用本软件产生的一切后果由用户自行承担。

- 开发者不对本软件适用性、稳定性作任何明示或暗示的保证，亦不对任何直接、间接、特殊、偶然或后果性损害承担责任
- 部分功能依赖第三方 API 与服务，使用者须自行确保其使用符合相关法律法规及服务协议
- 用户应在遵守校园网及相关网络工具使用条款和规定的前提下使用本软件，开发者不对用户违规使用的行为及其后果负责
- 因使用本软件而导致的账号被限制、设备被隔离或其他损失，开发者不承担任何责任

完整条款见 [用户协议与免责声明](docs/agreement.md) 与 [隐私政策](docs/privacy.md)。使用本软件即表示你已充分理解并同意上述全部条款。

## 许可证

本项目采用 [MIT](LICENSE) 许可，第三方组件许可见 [THIRD-PARTY.txt](THIRD-PARTY.txt) 与 [LICENSES/](LICENSES/)
