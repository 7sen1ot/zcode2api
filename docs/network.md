# 代理与上游安全验证

[返回 README](../README.md) · [配置说明](configuration.md)

## 出站代理

在「设置 → 出站代理」启用代理，加载节点后选择并保存。账号单独设置的代理优先；程序不读取 Windows 系统代理。

- 手填模式支持 HTTP/HTTPS 地址，每行一个，可带用户名和密码。
- 订阅模式支持 HTTPS 订阅中的 URI / Base64 URI 列表：VLESS TCP/WS（含 Reality）、Hysteria2、HTTP/HTTPS，最多 64 个节点。
- 优先使用指定节点，否则选择第一个可用节点。正常请求保持当前节点，故障时顺序切换；全部不可用时返回 `proxy_pool_exhausted`。
- 故障状态保存在内存中：连接故障隔离 30 秒，明确的 WAF 拦截暂避 60 秒，到期后允许后续请求重新尝试。修改配置或重启后重置；订阅刷新保留仍存在节点的状态。

只有代理拨号失败、代理 DNS 故障或代理认证失败等具有代理连接证据的错误，才按连接故障隔离节点。明确的 WAF 拦截单独标记为「上游拦截」，可暂避该出口，但不证明代理连接失效。两者在推理尚未输出时均可切换节点，计入普通重试次数和预算。SDK 的 `F001` 仅表示 SDK 风控拒绝，不能据此归因账号或代理，不触发节点切换。`3012`（`request has been blocked due to unusual activity`）按账号风控拒绝处理，不剔除代理，本次请求不再尝试被拒账号；尚未输出时可切换其他可用账号，受剩余故障重试预算约束。此处理不代表已确认账号永久封禁，不自动禁用账号。鉴权、额度、参数、并发暂忙、限流、响应读取中断和普通上游错误也不会剔除节点。

配置获取、SDK 验证与携票请求使用同一代理入口。外部代理若自行轮换公网 IP，需在提供商端配置固定或粘性会话。

验证码配置请求和代理探测不携带账号令牌，返回风控拒绝不能证明具体账号有问题。配置阶段的此类拒绝单独记为 `captcha_config_risk_blocked`，不轮换账号或隔离代理；探测结果通过 `transport_reachable` 区分连接已建立与业务可用。

将以下字段合并进 `config.json`：

```json
{
  "outbound_proxy": {
    "enabled": true,
    "source": "manual",
    "urls": ["http://127.0.0.1:7890"],
    "subscription_url": "",
    "mihomo_path": "mihomo",
    "preferred_node": "",
    "refresh_interval_sec": 3600
  }
}
```

未配置 `outbound_proxy` 时，旧 `proxy_url` 会迁移为单地址代理。旧 `rotation_enabled`、`rotation_interval_sec` 不再控制轮换；配置文件仍兼容 `subscription_filter`，界面保存时清除此字段。

### Mihomo

订阅模式需要 Mihomo。在设置页安装或填写已有程序路径。在线安装从[官方 GitHub](https://github.com/MetaCubeX/mihomo/releases/tag/v1.19.33)下载 `v1.19.33`，校验 SHA-256 后解压到数据目录的 `tools/mihomo/`，支持 Windows、Linux、macOS 的 amd64/arm64。

默认命令 `mihomo` 依次从 PATH、数据目录托管版本和 `--root` 目录查找；自定义相对路径以 `--root` 为基准。自动检测只检查程序，不安装或启动；安装需要手动点击，完成后保存路径。

每个节点有独立的本机 HTTP 入口。订阅按需刷新，正在进行的请求保留旧入口至结束；刷新失败时沿用旧配置。停止服务或切换配置后清理闲置子进程及运行文件。

### 连通性检测

「检测当前代理配置」使用未保存的配置，不发送账号令牌或消耗模型额度。接口返回 401 可证明出口可达，但不代表实际业务能通过上游验证。代理地址和订阅链接不写入浏览器草稿，节点凭据保存在管理员配置及私有运行目录。

## 安全验证

网页手动领取或全部领取先尝试提交；只有上游明确要求验证时，才由 Node.js 与 jsdom 运行官方 SDK，成功后在原出口携新票据重试一次。服务器需要人工交互、缺少运行环境或 SDK 验证未通过时，网页自动打开阿里云官方组件供用户完成验证。账号风控、代理故障和限流不会被误导到验证码流程。此行为不恢复定时领卡。

后台批处理领卡遇到 `3007` 等明确验证要求后，获取新票据并在原出口重试一次。每张卡独立取票，同一账号的领取串行化；账号风控、鉴权失败或人工验证要求会停止该账号剩余领取，继续处理其他账号。验证失败与网络异常写入领取记录；领取请求响应中断时不自动重发，应刷新领取状态后再决定。此流程独立于下面的推理模式，手动接口通过 `auto_verify: true` 启用同样的 SDK 处理，已提供浏览器票据时不重复运行 SDK。

### 推理模式

| `http.captcha.mode` | 行为 |
| --- | --- |
| `off`（默认） | 推理不运行验证 SDK |
| `auto` | 遵循上游 `enabled` 和 `skip_model_request`，明确要求验证时强制取票 |
| `always` | 场景启用时，每次推理前取票 |

推理票据失效且尚未输出时，在原出口重新验证一次。SDK 缺依赖、初始化错误、超时、未知拒绝或人工验证要求不会剔除代理节点。SDK 资源 HTTP 错误、一般网络错误分别报告；只有错误明确指向代理连接或认证失败时才隔离并切换代理。

### 票据预取续期

官方要求每张验证票据一次性使用，且行为验证完成到业务验签的间隔不得超过 90 秒（F008/F018/F019）。因此票据不跨请求复用；程序的做法是消费即预取：一张票据被真实请求使用后，按该出口（origin、版本与代理三元组）异步铸造下一张备用，同一出口的连续验证需求（批量领卡、`always` 模式推理）直接取用预取票据，不再等待 Node 冷启动。预取沿用同一全局单槽与原出口代理，失败不重试、不影响本次请求。每个出口至多保留一张预取票据，`ticket_ttl_ms` 控制预取票据的有效期（默认 75000 毫秒，上限 90000 对齐官方窗口，设为 0 关闭预取）。票据过期未使用即作废；票据失效或风控拒绝时仅作废该出口的场景缓存与预取票据，不影响其他出口。

### 依赖与配置

安装 Node.js 18+，在项目根目录运行：

```sh
npm --prefix captcha_node ci --omit=dev --ignore-scripts
```

源码和发行包须保留 `captcha_node/package.json` 与锁文件。程序不会自动安装 npm 包。

```json
{
  "http": {
    "captcha": {
      "mode": "auto",
      "node_path": "node",
      "jsdom_path": "",
      "simulate_canvas_webgl": true,
      "timeout_ms": 30000,
      "ticket_ttl_ms": 75000
    }
  }
}
```

`jsdom_path` 留空使用 `--root` 下的 `captcha_node`，也可指定包含 `node_modules/jsdom` 的目录。设置页自动检测 Node.js 与 jsdom，检测不访问上游或更改配置。

`simulate_canvas_webgl` 为 jsdom 补充 Canvas、WebGL 等模拟接口；可关闭以对照验证结果。模拟环境不保证通过上游判断。无需 Chrome；旧 `backend`、`browser_path` 配置会被忽略。

SDK 支持直连及带或不带用户名密码的 HTTP/HTTPS 代理，沿用 jsdom 的代理传输能力。票据仅通过内存、浏览器请求或子进程管道传递，不写入日志或数据库，也不跨请求复用。

### Docker

需要后台验证时加入 `CAPTCHA_RUNTIME=1`；需要订阅代理时加入 `PROXY_RUNTIME=1`。两者默认均为 `0`：

```sh
docker compose build --build-arg CAPTCHA_RUNTIME=1 --build-arg PROXY_RUNTIME=1
docker compose up -d
```

## 故障排查

- **HTTP 405**：仅凭 405 无法判断原因。`3012` 显示为「账号风控拒绝」，不触发代理切换；`upstream_waf_blocked` 表示明确的安全网关拦截。查看错误码、请求 ID 和追踪 ID。
- **领卡 3007**：本次领取未通过安全验证，可在领卡页面重试。
- **SDK 不可用**：检查设置页的 Node.js、jsdom 路径与版本。
- **需要人工验证**：按浏览器组件提示完成，或到 ZCode 客户端领取后刷新余额。

接口依据[阿里云客户端文档](https://help.aliyun.com/zh/captcha/captcha2-0/user-guide/new-architecture-for-web-and-h5-client-access)和[一次性票据约束](https://help.aliyun.com/zh/captcha/captcha2-0/user-guide/server-access)实现；参考项目：[liu5269/zcode2api](https://github.com/liu5269/zcode2api)。
