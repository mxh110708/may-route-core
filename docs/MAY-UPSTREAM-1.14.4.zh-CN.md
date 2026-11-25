# May Core 同步 sing-box 1.14.4

2026-11-25，Core 合入 [官方稳定标签 v1.14.4](https://github.com/SagerNet/sing-box/releases/tag/v1.14.4)，配套桌面为 **1.14.4-may.1**、Go **1.26.10**。本记录覆盖源码与隔离验收；源码推送、配套发行和本机安装分别核对，发行不自动安装。

## 来源与保留项

- 官方目标：`c005f35300ce76427dc285481ec5695f5b179cd2`；原官方基础 v1.14.2 为 `af6e64c3b69e6132ebaee0e1a3d24e93903f6709`。
- 原 May Core：`46291b7af391304b1dba227ca9537c98b0d3e4e7`；普通合并保存定制历史。恢复引用为 `backup/before-singbox-1.14.4-20261125`。
- 保留 May 的独立 Windows 服务／IPC、代理归属修复、工作进程回收、单成员代理组展示和[原生 XHTTP](MAY-XHTTP.zh-CN.md)。
- 桌面版本元数据为 1.14.4、revision 1；基础版本改变时 May 修订重新从 1 开始。Dashboard 固定 gitlink 不变。

Custom CI 固定 Go 1.26.10，确保本地正式构建与远端定制回归使用相同工具链。

官方差异包含 97 个提交、330 个路径变化，涉及分阶段 Scope 生命周期、连接关闭、嵌套代理组与 URLTest、DNS 缓存过滤、拨号顺序、Windows 双栈／IPv6、内置证书及锁定依赖更新。完整跨平台源码不等于 May 已完成所有平台、驱动或服务端部署验收。

## XHTTP 与新接口

HTTP/2／HTTP/3 XHTTP 实现 `V2RayMultiplexClientTransport` 和 `IdleConnectionKeeper`：参与预热，释放未引用出站的闲置连接，保留活动流，并在新拨号时重建池。预热的 keep-session 标记防止测量前丢弃刚建立的会话；网络重置继续沿用闲置策略。

两种复用物理连接都使用 `ContextForMultiplexSession`，使它们不继承某条业务流的外部中断和 DNS 归属。VLESS TLS／Reality 验证、XMux、独立下载身份和有界上传保持原支持范围。

`transport/v2rayxhttp/upstream_lifecycle_test.go` 通过实际 TLS 双流验证闲置清理、重连、预热复用和 HTTP/2 上下文；HTTP/3 测试同时核对上下文及重置。它们不会连接已安装服务。

## 验收与复跑

以桌面固定 Go 1.26.10 及 `release/DEFAULT_BUILD_TAGS_OTHERS`／`release/LDFLAGS` 运行：

- 模块校验、Windows CLI 和 May daemon 编译通过。
- 完整包回归：25 个含测试包，246 个顶层用例与 262 个子用例通过。
- XHTTP 专项：26 个顶层与 16 个子用例通过；不含 QUIC 的路径为 23 个顶层与 16 个子用例通过。
- Windows Race detector：25 个顶层与 9 个子用例通过。
- 桌面 259 个单元测试、类型检查及静态构建通过。
- `tests/xhttp-matrix.py` 的 47 个实际回环组合通过；Vision／AnyTLS、双用户、应用 UDP、并发及十种错误身份／路径负例通过。
- `tests/xhttp-lifecycle.py` 联合生产 Route 准备与故障判定，验证真实 StartedService 的初始化、切换、测速、四模式重载、活动取消和停止／重启。

首次完整测试中的 3 个公网 TLS 分片用例连接失败，8 个依赖 WinDivert／SCM 的用例无权限。最终完整包回归仅精确排除这些环境用例；另有真实签名 EXE 资源用例因无夹具跳过。没有申请驱动权限、安装驱动或修改系统路由来消除测试限制。

生命周期夹具首次误用源码目录中已有的合成组选择缓存。`tests/xhttp_daemon` 现将进程工作目录设到显式的一次性目录，`tests/xhttp-lifecycle.py` 断言初始节点确为错误身份；重跑通过且原缓存哈希不变。`xhttp-runtime.py --baseline` 现在可选，不传时只验证连通性，记录中不生成资源比较。

复跑脚本仍使用各自 `--help` 参数。所有证书、用户和监听均为临时回环夹具，完成后自有进程及临时目录均清理；无需操作本机已安装的 May 服务、系统代理或 TUN。

官方 Go ARM64 补丁保持上游原始字节，因此基于旧 HEAD 的空白检查会报告该嵌入补丁的上游空白；May 相对 v1.14.4 的自有差异检查通过。
