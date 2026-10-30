# May 原生 XHTTP 客户端

2026-10-29，第二十轮开发源码。需要包含此扩展的 May Core 与配套 Route；旧安装和上游 sing-box 1.14.2 不会因为加入配置字段而获得支持。当前没有推送、发行或安装更新。

本扩展使用原有 VLESS／XUDP、拨号器、TLS／Reality 和 HTTP 库，提供 VLESS XHTTP **出站**；服务器继续使用 Xray，不引入第二常驻核心。固定协议参照 Xray **26.3.27**，线格式辅助代码适配自 Mihomo **1.19.32**，构建工具链为 Go **1.26.8**。归属及许可证见 [wire helpers](../common/xhttpwire/THIRD-PARTY.md)。

## 模式、HTTP 和身份

| 安全方式 | HTTP/1.1 | HTTP/2 | HTTP/3 |
| --- | --- | --- | --- |
| 验证证书的 TLS | 支持，TLS 1.2 及以上 | 支持，TLS 1.2 及以上 | 支持，QUIC／TLS 1.3，需要 `with_quic` |
| Reality | 不适用 | 支持，TLS 1.3、正确公钥／short ID／SNI | 固定上游不适用，明确拒绝 |

各合法版本均覆盖 `stream-one`、`stream-up`、`packet-up`、`auto`。应用 UDP 通过 VLESS XUDP 承载；外层只有 HTTP/3 使用 UDP，HTTP/1.1 和 HTTP/2 使用 TCP。不要根据应用 UDP 成功就认定外层是 QUIC。

| 模式 | 行为 |
| --- | --- |
| `stream-one` | 单个双向 HTTP 请求，上传用管道背压；不适用独立下载端点。 |
| `stream-up` | 上传流和下载请求分开，可配置独立下载地址及身份。 |
| `packet-up` | 上传按序分片，队列受限；下载保持流式，可配置独立下载端点。 |
| `auto`／省略 | TLS 选择 packet-up；Reality 无独立下载时选择 stream-one，有独立下载时选择 stream-up。 |

`http_version` 可为 `1.1`、`2`、`3`、`auto` 或省略。auto 是本地版本选择：单独配置 ALPN `http/1.1` 时选 1.1，单独配置 `h3` 时选 3，其余选 2；随后必须实际使用该版本。它不会在失败时探测其他版本、放宽身份验证或改为直连。显式版本与 ALPN 冲突会被拒绝。

普通 TLS 的 HTTP/2 必须协商 `h2`；固定 Xray Reality 服务有意不返回 ALPN，因此只对已由原有 Reality 拨号器完成鉴权的连接接受空 ALPN，并继续验证真实 HTTP/2 回应。普通 TLS 不借此跳过 ALPN 检查。

## 原生配置及参数关系

下面是完整结构示例，需先替换占位 UUID、连接地址和身份。`example.test` 域名不会提供真实服务。示例只开本机 mixed，不设置系统代理或 TUN。

```json
{
  "dns": {
    "servers": [{"type": "local", "tag": "dns-bootstrap"}],
    "final": "dns-bootstrap"
  },
  "inbounds": [{"type": "mixed", "listen": "127.0.0.1", "listen_port": 2080}],
  "outbounds": [{
    "type": "vless",
    "tag": "XHTTP Entry",
    "server": "upload.example.test",
    "server_port": 443,
    "uuid": "<UUID>",
    "packet_encoding": "xudp",
    "tls": {
      "enabled": true,
      "server_name": "identity.example.test",
      "alpn": ["h2"]
    },
    "transport": {
      "type": "xhttp",
      "http_version": "2",
      "mode": "packet-up",
      "host": "front.example.test",
      "path": "/stream/?keep=a%2Fb",
      "extra": {
        "sessionPlacement": "header",
        "sessionKey": "X-Session",
        "seqPlacement": "query",
        "seqKey": "sequence",
        "scMaxEachPostBytes": "2048-4096",
        "scMinPostsIntervalMs": 30,
        "xmux": {"maxConcurrency": 2, "hMaxRequestTimes": "600-900"}
      }
    }
  }],
  "route": {"default_domain_resolver": "dns-bootstrap", "final": "XHTTP Entry"}
}
```

连接地址决定拨号目的地，SNI 决定 TLS／Reality 身份，Host 和路径决定 HTTP 路由，UUID 决定 VLESS 用户；它们互不替代。Host 可带端口，空值跟随 SNI，不能含 URL 或控制字符。路径最长 8192 字符，保留查询串与百分号转义，并按固定 Xray 的规则补齐首尾斜线；已转义的路径段不会预先解码。

Reality 使用相同出站和传输结构，将 `tls` 改为：

```json
{
  "enabled": true,
  "server_name": "identity.example.test",
  "alpn": ["h2"],
  "utls": {"enabled": true, "fingerprint": "chrome"},
  "reality": {
    "enabled": true,
    "public_key": "<REALITY_PUBLIC_KEY>",
    "short_id": "<SHORT_ID>"
  }
}
```

保持空 flow，不附加 Vision、VLESS 多路复用或 kTLS。不允许 `insecure`、禁用 SNI 来绕过身份校验。HTTP/3 使用 QUIC TLS，TCP uTLS 指纹不应用于 QUIC；Reality 则需要其配套 uTLS。

## 独立下载端点

将以下对象放在 `transport.download_settings`；主模式须为 stream-up、packet-up 或 auto。下载侧不是另一个 VLESS 用户，仍属于当前会话，但连接地址、TLS／Reality、SNI、HTTP 版本、Host、路径、请求头、元数据和 XMux 都可独立配置。

```json
{
  "server": "download.example.test",
  "server_port": 8443,
  "tls": {
    "enabled": true,
    "server_name": "download-identity.example.test",
    "alpn": ["h3"]
  },
  "transport": {
    "type": "xhttp",
    "http_version": "3",
    "host": "download-front.example.test",
    "path": "/download/?keep=%2F",
    "extra": {
      "sessionPlacement": "cookie",
      "sessionKey": "download_session",
      "xmux": {"maxConnections": 2, "hMaxRequestTimes": 200}
    }
  }
}
```

两侧须经服务端或反向代理汇入同一个会话系统。独立 Host／路径／元数据并不让两个互不关联的 Xray 入站自动共享会话。两侧分别校验身份，下载侧认证失败不能沿用上传侧身份或回退；不允许递归嵌套下载设置。

原生 May 配置使用上述结构；标准 Xray 分享 URI 的下载信息位于 `extra.downloadSettings`。Deploy 负责两者转换，不能把 Xray streamSettings 直接塞进原生 `extra`。

## 固定高级字段清单

原生 `extra` 使用固定 Xray 的 camelCase 字段名。范围可写整数、`"min-max"` 或 `{"from": min, "to": max}`；顺序、类型、缓冲上限和枚举会校验，未知字段明确拒绝。

| 字段 | 作用及限制 |
| --- | --- |
| `headers` | 自定义请求头；最多 64 项，拒绝重复大小写键、控制字符及 Host／Content-Length／Transfer-Encoding／Connection。 |
| `xPaddingBytes` | 填充范围；0 恢复固定协议默认 100–1000，非零范围须为正数。 |
| `xPaddingObfsMode` | 启用可配置的填充位置和方式。 |
| `xPaddingKey`、`xPaddingHeader` | 默认 `x_padding`、`X-Padding`。 |
| `xPaddingPlacement` | `header`、`cookie`、`query`、`queryInHeader`；默认 queryInHeader。 |
| `xPaddingMethod` | `repeat-x` 或 `tokenish`；默认 repeat-x。 |
| `noGRPCHeader` | 控制上传请求的 GRPC 内容类型头。 |
| `uplinkHTTPMethod` | 上传方法，默认 POST；GET 必须明确使用 packet-up。 |
| `sessionPlacement`、`sessionKey` | 会话元数据位于 path／header／cookie／query；默认 path。 |
| `seqPlacement`、`seqKey` | 上传分片序号，位置同上；默认 path。 |
| `uplinkDataPlacement`、`uplinkDataKey` | body／header／cookie／auto；header／cookie 数据只适用明确的 packet-up。 |
| `uplinkChunkSize` | 请求头／Cookie 中的数据块范围，避免单个字段无限增大。 |
| `scMaxEachPostBytes` | 每次上传分片大小；0 使用默认 1,000,000 字节，缓冲惰性分配。 |
| `scMinPostsIntervalMs` | 上传最小间隔；0 使用默认 30 ms。 |
| `scMaxBufferedPosts` | 服务端缓冲参数保留；客户端也据此收紧在途请求，最多 4 个，背压阻止无限排队。 |
| `scStreamUpServerSecs`、`noSSEHeader`、`serverMaxHeaderBytes` | 固定服务端参数；保留并校验，不伪装成客户端本地功能。 |
| `xmux` | 连接复用与生命周期，见下文六项。 |

XMux 字段为 `maxConcurrency`、`maxConnections`、`cMaxReuseTimes`、`hMaxRequestTimes`、`hMaxReusableSecs` 和整数 `hKeepAlivePeriod`。前两项不可同时启用；逻辑会话复用次数、实际 HTTP 请求次数及可复用时长分别计数。到限后把新请求转到新连接，旧连接上的活动下载继续完成。

显式配置的 XMux 上限都会执行。省略／全零时，May 使用一个共享惰性池和 30 秒空闲回收，不照搬 Xray 的随机默认轮换，也不主动发周期 PING；显式正的保活周期才启用相应 HTTP/2／QUIC 保活。这个本地连接策略单列说明，不改变线格式或把未使用节点加入探测。

可能分配的单项上传／填充范围受 16 MiB 上限约束；不是把配置值作为预分配大小。大型响应头预算按已验证填充范围计算，不取消整体大小限制。Mihomo 特有的 `session-table`、`session-length` 不是本固定 Xray 字段集；Deploy 可保留它们用于 Mihomo，但不会有损转换为 May 配置。

## Route、重载和资源

Route 保留原生出站、独立下载设置、地区组及业务组关系；按需探测复用完整出站。系统代理和 TUN 两种准备模式有参数保留回归，真实 StartedService 测试覆盖加载、四种模式重载、测延迟、确认故障后选择备用、活动连接取消和停止／重启。测试没有切换本机代理或打开 TUN。

上传使用管道或有界分片，不完整缓冲业务流量。空闲物理连接 30 秒后回收，峰值产生的闲置逻辑池也回收。握手的拨号上下文只限制拨号和响应头，成功后不因调用者释放该上下文而中断活动流。连接关闭、错误、根上下文取消和网络重置会解除阻塞并清理连接池；不会为未知状态自动重放已提交的上传。

没有修改 Dashboard gitlink、捕获规则、安装身份、系统代理保持机制或现有节点参数，没有增加第二常驻核心。使用此功能需要随后按正常发行流程交付的配套完整桌面包。

资源优化限于本轮上传路径，全部使用后台命令行。packet-up 分片缓冲按需倍增，并以已校验的单次 POST 大小封顶；不按最大值预分配，也不把确认后的大缓冲保留在全局池。四次交换顺序的对比中，1／8 MiB 上传的累计分配量分别下降约 62%／55%，构造耗时下降约 66%／54%；4 KiB 请求基本不变。此项是构造路径微基准，不能推断整应用常驻内存或真实 WAN 吞吐的同等改善。方法、数据与限制见 Deploy [第二十轮记录](https://github.com/mxh110708/may-vps-deploy/blob/main/docs/XHTTP-ROUND-20-2026-10-29.zh-CN.md)。

先前显示测试窗口的四场景对照已中止并作废，900 秒观测未执行；两者不计入本轮验收。未来界面验证使用已有后台测试接口，不显示测试窗口。

## 实测与复跑

所有协议夹具只监听回环，按次生成 PKI／凭据，不写系统信任库。固定 Xray Windows 二进制先核对 SHA-256。

| 入口 | 证明范围 |
| --- | --- |
| `tests/xhttp-matrix.py` | TLS 三版本 × 四模式、Reality HTTP/2 四模式、元数据四位置、上传数据头／Cookie、八组填充、查询转义、XMux、GET／PUT、零值默认、TLS 1.2、大填充，及上下行分离。 |
| `tests/xhttp_proxy/` | 受控 HTTP/2／HTTP/3 反向代理；两侧地址及回程分别观测，不依赖公网 CDN。 |
| `tests/xhttp-runtime.py` | 两用户、HTTPS／应用 UDP、并发、十种错误身份／路径反例、既有 Vision 和 AnyTLS。 |
| `tests/xhttp-lifecycle.py` | 生产 Route 配置准备／探测／故障切换与真实 StartedService 重载、活动取消、停止和重启。 |
| `BenchmarkPacketUpload` | 后台对比 4 KiB 小请求和 1／8 MiB 分块上传的分配量与构造耗时，不包含网络 RTT。 |
| Go 包测试及 Race detector | 参数、错误状态码／重定向、HTTP 版本不符、背压、取消、连接复用／代际回收、网络重置、根上下文和真实 HTTP/3。 |

矩阵共 47 个实际连接场景，包括大小写／空 Reality 客户端 short ID、三组 TLS 分离版本、Reality／Reality 和两种混合 TLS／Reality 下载身份；错误下载身份不会抵达目标。运行要求与命令参数见各脚本 `--help`。Route 的配套脚本为桌面仓库 `tests/xhttp-runtime.ts`，使用该仓库固定 Node／依赖。

完整 Go 回归排除 3 项依赖公网的 TLS 分片测试及 8 项依赖 WinDivert／SCM 的测试；其余包通过。没有申请驱动权限或通过修改系统网络让这些环境用例通过。Linux QUIC 版本完成交叉编译，不能以编译替代 Linux 实际客户端连接。

公网服务商、特定 CDN、Cloudflare 普通代理或 Tunnel 尚未验收。受控反向代理通过只证明本轮所测流式路径；外部服务的端口、TLS 终止、HTTP 版本、缓冲、请求头与超时需分别核对。此文不声称所有 CDN 或任意高级组合通用。

## 固定上游参考

- [Xray 配置与默认值](https://github.com/XTLS/Xray-core/blob/v26.3.27/infra/conf/transport_internet.go)、[客户端](https://github.com/XTLS/Xray-core/blob/v26.3.27/transport/internet/splithttp/dialer.go)、[服务端](https://github.com/XTLS/Xray-core/blob/v26.3.27/transport/internet/splithttp/hub.go)、[XMux](https://github.com/XTLS/Xray-core/blob/v26.3.27/transport/internet/splithttp/mux.go)。
- [Mihomo 固定版 XHTTP](https://github.com/MetaCubeX/mihomo/tree/v1.19.32/transport/xhttp)、[许可证](https://github.com/MetaCubeX/mihomo/blob/v1.19.32/LICENSE)。
