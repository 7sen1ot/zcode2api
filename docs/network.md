# 代理与上游安全验证

[返回 README](../README.md) · [配置与重试](configuration.md) · [协议兼容](protocol-compatibility.md)

## 出站代理

在「设置 → 出站代理」选择来源、加载节点并保存：

- 手填模式支持 HTTP/HTTPS 地址，每行一个，可带用户名和密码。
- 订阅模式支持 HTTPS 订阅中的 URI / Base64 URI 列表：VLESS TCP/WS（含 Reality）、Hysteria2、HTTP/HTTPS，最多 64 个节点。

账号固定代理优先于全局代理池；账号未设代理且全局代理关闭时直连。程序不读取 Windows 系统代理。池中优先使用指定节点，否则选第一个可用节点；正常请求保持当前节点，不按时间或请求数轮换。

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

### 出口故障与切换

以下节点隔离与自动切换仅适用于已启用的全局代理池；账号固定代理没有备用节点池。

| 情况 | 处理 |
| --- | --- |
| 明确的代理拨号、DNS 或认证失败（`proxy_connection_failed` / `proxy_auth_failed`） | 隔离至少 30 秒，立即选择下一个可用节点 |
| `upstream_waf_blocked` | 单独标记为上游拦截，按上游地址与出口冷却至少 60 秒；连续拦截时翻倍，最长 15 分钟，并遵守更长的 `Retry-After`。可换出口，但不据此认定代理连接失效 |
| `3012` / `upstream_risk_blocked` | 按账号风控处理，本次请求跳过该账号，可换其他可用账号；不剔除代理或自动禁用账号，也不据此判定永久封禁 |
| `captcha_risk_blocked` / `F001` | SDK 风控拒绝，不提供账号或代理故障证据，不换节点 |
| 鉴权、额度、参数、并发暂忙、普通限流、响应中断等 | 不剔除节点；账号调度和请求重试见[配置说明](configuration.md#调度与重试) |
| 没有可用节点 | 返回 `proxy_pool_exhausted`；节点均被上游拦截时返回 `proxy_upstream_blocked`，不回退直连 |

故障切换记录节点名称与原因。尚未输出的推理可在原有次数和时间预算内换节点重试；管理请求更新后续选路，不自动重放领取等业务操作。验证及携票请求固定使用原入口，进行中的请求不会中途换节点；外部代理自行轮换公网 IP 时，需在提供商端配置固定出口或粘性会话。

隔离状态保存在内存，修改配置或重启后重置，订阅刷新保留仍存在节点的状态。到期节点可重新参与选路，但当前节点健康时不主动切回。

### Mihomo

订阅模式需要 Mihomo。在设置页安装或填写已有程序路径。在线安装从[官方 GitHub](https://github.com/MetaCubeX/mihomo/releases/tag/v1.19.33)下载 `v1.19.33`，校验 SHA-256 后解压到数据目录的 `tools/mihomo/`，支持 Windows、Linux、macOS 的 amd64/arm64。

默认命令 `mihomo` 依次从 PATH、数据目录托管版本和 `--root` 目录查找；自定义相对路径以 `--root` 为基准。自动检测不安装或启动程序，在线安装需手动点击并保存路径。

每个节点使用独立的本机 HTTP 入口。订阅按需刷新，旧入口保留至进行中的请求结束；同一订阅与运行参数下刷新失败，旧进程仍存活则继续沿用。停止服务或切换配置后清理闲置子进程及运行文件。

### 连通性检测

「检测当前代理配置」使用未保存的配置，不发送账号令牌或消耗模型额度。`transport_reachable` 表示传输是否建立；预期的 401 表示出口可达，不代表账号、模型或验证可用。匿名探测不能归因账号风控。

代理地址和订阅链接不写入浏览器草稿，节点凭据保存在管理员配置及私有运行目录。

## 安全验证

服务端通过 Node.js 与 jsdom 运行官方阿里云 SDK。推理验证策略与领卡验证独立；验证码配置查询不携带账号令牌，其中的一般风控拒绝记为 `captcha_config_risk_blocked`，不归因账号或隔离代理。

### 领卡验证

网页、批量和定时领取均先提交请求；上游明确要求验证后，运行服务器 SDK，在原出口携新票据重试一次。手动接口通过 `auto_verify: true` 启用此流程；已提供浏览器票据时不再重复运行 SDK。

网页遇到 SDK 依赖缺失、运行异常、验证未通过或人工交互要求时，尝试转入官方组件。组件初始化后等待 2.1 秒，自动尝试一次浏览器无痕验证；失败、15 秒无结果或要求交互时保留手动入口，不循环重试。网页批量领取逐张验证，关闭或取消弹窗即停止当前流程。

同一账号的领取串行化，每张卡独立取票。后台批处理遇到账号风控、鉴权失败或人工验证要求时，停止该账号后续领取并继续其他账号；定时任务保留需人工处理的结果，不自动弹出浏览器。执行时间与重启规则见[领卡行为](configuration.md#领卡行为)。

验证失败和网络异常写入领取记录；领取请求响应中断时不自动重发，需先刷新确认领取状态。账号风控、代理故障和限流不转入验证码流程。

### 推理模式

| `http.captcha.mode` | 行为 |
| --- | --- |
| `off`（默认） | 推理不运行验证 SDK |
| `auto` | 遵循上游 `enabled` 和 `skip_model_request`，明确要求验证时强制取票 |
| `always` | 场景启用时，每次推理前取票 |

推理验证已启用、票据失效且尚未输出时，在原出口重新验证一次。依赖缺失、超时、资源 HTTP 错误或未知 SDK 拒绝分别返回具体错误；只有[明确的出口故障](#出口故障与切换)才触发节点切换。

### 票据预取续期

票据按账号凭据、用途、场景和出口隔离，一张只用于一次业务请求。官方验签窗口上限为 90 秒（F008/F018/F019）；`ticket_ttl_ms` 默认 75000 毫秒、上限 90000 毫秒，提交前另留安全余量，设为 0 只关闭预取。

同一隔离范围内出现连续成功验证需求后，按需预取下一张；单次成功不触发后台取票。每池至多保留一张备用票据。预取使用原出口和共享的单个 SDK 槽位，前台等待者优先；失败不重试，不影响已完成的业务结果。

过期、已提交或被拒的票据不再使用；凭据、用途、场景或出口变化后不能沿用旧票据。共享冷却暂停受影响范围的预取，其他票据池独立管理。票据只在内存、请求或进程管道中传递，不写入日志或数据库。

### 依赖与配置

服务端验证需安装 Node.js 18+，无需 Chrome。在项目或发行包根目录安装 jsdom：

```sh
npm --prefix captcha_node ci --omit=dev --ignore-scripts
```

源码和发行包须保留 `captcha_node/package.json` 与锁文件。

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

`node_path` 指定 Node.js；`jsdom_path` 指向包含 `node_modules/jsdom` 的目录，留空使用 `--root` 下的 `captcha_node`。设置页环境检测不访问上游或修改配置，程序也不自动安装 npm 依赖。

`simulate_canvas_webgl` 补充 Canvas、WebGL 模拟接口，不保证通过上游判断；旧 `backend`、`browser_path` 配置已忽略。

SDK 支持直连及带或不带认证的 HTTP/HTTPS 代理；订阅节点通过 Mihomo 的本机 HTTP 入口接入。

### Docker

需要后台验证时加入 `CAPTCHA_RUNTIME=1`；需要订阅代理时加入 `PROXY_RUNTIME=1`。两者默认均为 `0`：

```sh
docker compose build --build-arg CAPTCHA_RUNTIME=1 --build-arg PROXY_RUNTIME=1
docker compose up -d
```

## 故障排查

排查时保留 HTTP 状态、错误码、请求 ID 和追踪 ID；仅凭 403 或 405 无法判断原因。代理故障、WAF、`3012` 和 `F001` 的分类见[出口故障与切换](#出口故障与切换)，其余常见错误如下：

| 错误码 / 提示 | 含义与处理方式 |
| --- | --- |
| `3008` / `3009` / `3010` | 上游账号并发暂忙，按[调度与重试](configuration.md#调度与重试)等待或使用其他可用账号 |
| `account_busy` | 网关等待账号槽位超时；检查账号数量、并发占用和排队时长 |
| `rate_limit_error` / 请求频率受限 | 遵守等待时间和重试窗口，不因此认定代理损坏 |
| `authentication_error` / `insufficient_quota` | 分别为上游凭据失效、账号额度不足；重新授权或补充可用额度，本次请求可使用其他账号 |
| `coding_plan_security_verification_required`，或带明确验证提示的 `3007` | 进入 SDK 或人工验证流程；`3007` 需结合提示判断，不一律视为验证码问题 |
| `captcha_ip_rate_limited` / `F010` | SDK 出口访问频率受限，按出口作用域冷却 |
| `captcha_device_rate_limited` / `F011` | SDK 设备访问频率受限，按设备作用域冷却 |
| `captcha_expired` / `F019` | 验证票据已失效，需要新票据 |
| `F008` / `F018` / `captcha_duplicate_result` | 验证票据重复提交，不能复用已提交的结果 |
| `captcha_jsdom_missing` / `captcha_runtime_error` / `captcha_timeout` | SDK 依赖缺失、运行异常或验证超时；检查 Node.js、jsdom、网络和验证配置 |
| `first_token_timeout` / `stream_idle_timeout` / `inference_timeout` | 分别为首个有效输出、流式输出空闲、整轮推理超时 |
| `proxy_subscription_auth_failed` | 订阅令牌可能错误或已失效，重新复制有效订阅链接 |

SDK 不可用时先查看设置页的环境检测结果和依赖路径；需要人工验证时使用官方组件，或在 ZCode 客户端完成领取后刷新余额。

接口依据[阿里云客户端文档](https://help.aliyun.com/zh/captcha/captcha2-0/user-guide/new-architecture-for-web-and-h5-client-access)和[一次性票据约束](https://help.aliyun.com/zh/captcha/captcha2-0/user-guide/server-access)实现；参考项目：[liu5269/zcode2api](https://github.com/liu5269/zcode2api)。
