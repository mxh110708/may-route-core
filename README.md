# May Route Core

[May Route](https://github.com/mxh110708/may-route-desktop) 配套的 sing-box 核心源码，负责协议处理、Windows 服务及本地通信集成。最终桌面应用、安装器、更新和故障切换策略由 desktop 仓库维护；本仓库不单独提供供普通用户安装的 May 桌面包。

[English](README.en.md) · [配套开发说明](docs/MAY-CORE.zh-CN.md) · [上游协议参考](https://sing-box.sagernet.org)

## 使用哪一份代码

当前维护分支为 `custom-main`，配套桌面文档基线 1.14.2-may.26。应使用桌面构建记录选定的核心版本、来源提交和工具链，不从任意分支临时编译后替换已安装服务。普通用户在 [desktop Release](https://github.com/mxh110708/may-route-desktop/releases/latest) 下载完整安装器。

保留 `github.com/sagernet/sing-box` 模块、协议字段和必要文件名；May 的应用／服务／IPC 与上游安装身份隔离。具体实现以所选提交为准，不能把名称隔离理解成通用迁移导入承诺。

## 文档边界

`docs/` 大部分页面来自上游 sing-box 的配置和协议参考，它们解释核心语法，不代表 May 桌面为所有平台、入站协议、服务端部署或管理扩展提供完整界面和验收。

- 桌面模式、配置加载、Windows 代理、资源与故障切换：[May Route 文档](https://github.com/mxh110708/may-route-desktop/blob/custom-main/docs/README.md)。
- VPS 部署、用户／证书、转发和线路编排：[May VPS Deploy](https://github.com/mxh110708/may-vps-deploy)。
- 本仓库与构建、测试关系：[核心开发说明](docs/MAY-CORE.zh-CN.md)。

本项目源自 [SagerNet/sing-box](https://github.com/SagerNet/sing-box)，不是 SagerNet 官方发行。保留 [LICENSE](LICENSE)、上游版权及命名限制。仓库不得包含私人代理配置、节点凭据、数据库、密钥或签名材料。
