# 内嵌 frpc

此 custom 版本将 frp v0.62.1 客户端编译进 mihomo，复用 mihomo 的
Shadowsocks listener。运行时只有一个 mihomo 进程，无须部署本地 frpc
或额外的 SS server。远端仍需部署 frps；已用 v0.62.1 验证兼容性。

## 配置与迁移

将 [frpc.yaml](frpc.yaml) 中的 `listeners` 和 `frpc` 段合并到现有配置。
替换 frps 地址、frp token 和 SS 密码。不要将真实凭证提交到仓库。

数据路径：外部 SS 客户端 → frps 的 TCP/UDP 端口 → 内嵌 frpc →
本机 loopback SS listener → mihomo 出站。

- 将原 frpc 的 `socks5` 插件改成示例中的 TCP、UDP 两条代理。
- 两条代理名称必须不同，可以使用相同数字的 `remotePort`。
- frps 需允许相应端口，服务器防火墙需放行该端口的 TCP 和 UDP。
- 外部客户端改用 SS，并启用 UDP。SS 密码与 frp token 是不同的凭证。
- `proxy: DIRECT` 保持原 socks5 插件的直连出口行为；移除后使用 mihomo 规则。
- 停止旧的独立 frpc 后再切换，以免旧实例继续注册同名代理或占用远端端口。

## 第一版支持范围

`frpc` 内保留原生 frpc 的 camelCase 字段名，只支持示例中列出的字段。

| 字段 | 约束 / 默认值 |
| --- | --- |
| `enable` | 默认 false；缺省整个段也不启动 frpc |
| `serverAddr`、`serverPort` | 启用时必填；地址为 IP 或主机名，不带协议和端口 |
| `auth.method`、`auth.token` | method 默认 token，只支持 token；token 必须非空 |
| `loginFailExit` | 默认 false，只接受 false |
| `transport.protocol` | 默认 tcp，只支持 tcp；内部使用原生 TCP 多路复用 |
| `transport.tls.enable` | 默认 true；与原 frpc 仅开启 TLS 的行为一致 |
| `log.to`、`log.level` | to 仅 console；level 默认 info，可选 trace/debug/info/warn/error |
| `proxies[].name` | 非空且唯一 |
| `proxies[].type` | tcp 或 udp |
| `proxies[].localIP` | 默认 127.0.0.1；只接受数值 loopback 地址，包括 ::1 |
| `proxies[].localPort`、`remotePort` | 1–65535；同一协议下不能重复使用 remotePort |

启用时至少需要一条代理。未知字段、重复字段、错误类型和未支持功能会明确报错。
不支持 plugins、visitors、OIDC、QUIC/KCP/WebSocket、证书文件配置、frp 管理
接口、虚拟网卡、额外配置文件或 frp 的 DNS 覆盖。YAML 普通别名可用，frpc
段不接受 `<<` 合并键。UDP 使用 frp 原生的封装和默认数据报大小限制。

首版 TLS 配置与原配置相同：启用加密但未配置 CA 校验。frp 的其他 TLS 字段
不在本版范围内。

## 启动、日志与重载

frpc 的本地校验在 mihomo 应用配置之前完成，`mihomo -t -f config.yaml`
也会进行此项校验。frpc 校验不解析服务器地址、不发起连接、不验证远端 token
或端口是否可用。mihomo 其他配置原有的资源加载行为不变。

合法配置异步启动客户端。首次离线、超时或认证失败都不会阻止 mihomo 启动。
客户端在后台持续重试，运行中断线后自动重连并重新注册代理。端口注册失败
由 frp 自身记录并重试；远端恢复后，无须重启 mihomo。

日志通过 mihomo 的日志系统和日志事件接口输出，前缀为 `[FRPC]`。frpc 的
`log.level` 与 mihomo 的全局日志级别共同决定输出级别。认证 token 不输出。

- 非法重载在解析阶段被拒绝，当前运行实例保留。
- 相同的归一化 frpc 配置保留现有实例和连接。
- 修改配置时取消旧实例，待其关闭后异步启动新实例，可能有短暂转发中断。
- 禁用或删除 frpc 段会停止客户端；本地 SS listener 独立管理。
- 退出 mihomo 时先取消 frpc 并等待清理，再关闭本地 listener。

## 嵌入适配

生产路径仅调用 Go 库，不执行 frpc 命令，不创建 frpc 子进程。
[`third_party/frp/MIHOMO.md`](../third_party/frp/MIHOMO.md) 记录来源和依赖补丁：
导入客户端时不再修改进程级 QUIC 环境变量或注册默认 HTTP pprof 路由。
frpc 不替换 `net.DefaultResolver`，
连接 frps 使用 mihomo 的网卡绑定、socket hook 和 bootstrap 解析，并忽略
`http_proxy`。本地转发目标限制为 loopback，避免使用 frp 原生后端拨号解析主机名。

frp 自身包级日志、注册表和加密 salt 仍按协议使用。依赖也有内部缓存、计时器；
首版不开启 frp 管理服务。这些不意味着
增加独立进程或自动开放管理端口。单进程仍共享 CPU、内存和进程级故障范围。

## 构建与验证

首版部署目标为 macOS arm64。依赖要求 Go 1.23+，开发验证使用 Go 1.26.4。
原项目 Go 1.20–1.22 的兼容构建不适用于此 custom 版本。

```sh
go test -race -tags with_gvisor -count=1 -timeout=120s ./component/frpc/... ./config
./scripts/build-custom-macos.sh
./bin/mihomo-darwin-arm64 -t -f docs/frpc.yaml
```

构建脚本默认注入版本 `v1.19.31-custom`，生成可执行文件
`bin/mihomo-darwin-arm64` 和压缩包
`bin/mihomo-darwin-arm64-v1.19.31-custom.gz`。更新版本时可通过
`VERSION=vX.Y.Z-custom ./scripts/build-custom-macos.sh` 覆盖。

这是独立命令行程序，没有 `.app`、`Info.plist` 或 Bundle ID。脚本在构建后
使用 macOS `codesign` 将代码签名标识设为 `mihomo`，替换 Go 链接器的
默认标识 `a.out`。使用的是 ad-hoc 签名，不依赖开发者证书，没有 Team ID，
也不包含 Apple 公证；代码签名标识不是 Bundle ID。
签名步骤需要在 macOS 上执行。可用以下命令检查产物：

```sh
./bin/mihomo-darwin-arm64 -v
codesign -dvv bin/mihomo-darwin-arm64
codesign --verify --strict --verbose=2 bin/mihomo-darwin-arm64
```

集成测试仅使用临时目录、loopback 端口和测试凭证。测试用 frps 作为独立
fixture 进程运行；frpc 与 SS 始终运行在同一进程内。测试覆盖实际 SS 的 TCP/UDP
转发、远端首次离线、frps 重启、非法重载、端口修改、认证失败日志和禁用。
连接单测覆盖 mihomo 网卡策略和握手中取消。它们不会启用真实 TUN 或修改
主机路由；真实 TUN 环境需要在部署配置下另行验证。

以后更新 mihomo 时在 `custom` 合并新的正式 release tag；frp 依赖保持独立
固定版本，只在需要升级时更新。构建版本号相应改为新的 release 加 custom 后缀。
