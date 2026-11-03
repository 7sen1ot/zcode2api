# 代理与上游安全验证

[返回 README](../README.md) · [配置说明](configuration.md)

## 出站代理

在「设置 → 出站代理」启用代理，加载节点后选择并保存。账号单独设置的代理优先；程序不读取 Windows 系统代理。

- 手填模式支持 HTTP/HTTPS 地址，每行一个，可带用户名和密码。
- 订阅模式支持 HTTPS 订阅中的 URI / Base64 URI 列表：VLESS TCP/WS（含 Reality）、Hysteria2、HTTP/HTTPS，最多 64 个节点。
- 优先使用指定节点，否则选择第一个可用节点。正常请求保持当前节点，故障时顺序切换；全部不可用时返回 `proxy_pool_exhausted`。
- 剔除状态保存在内存中，修改配置或重启后重置；订阅刷新会保留仍存在节点的状态。

WAF、`3012`、推理 SDK 的 `F001` 拒绝和连接故障可触发全局代理池切换。推理尚未输出时可重试，计入普通重试次数和预算。鉴权、额度、参数、并发暂忙、限流和普通上游错误不会剔除节点。

配置获取、SDK 验证与携票请求使用同一代理入口。外部代理若自行轮换公网 IP，需在提供商端配置固定或粘性会话。

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

网页领卡使用浏览器中的阿里云官方组件。后台领卡和推理验证使用 Node.js 与 jsdom；依赖缺失或上游要求人工操作时，转到领卡页面或官方客户端处理。

后台领卡遇到 `3007` 等明确验证要求后，获取新票据并在原出口重试一次。每张卡独立取票，验证失败写入领取记录。此流程独立于下面的推理模式。

### 推理模式

| `http.captcha.mode` | 行为 |
| --- | --- |
| `off`（默认） | 推理不运行验证 SDK |
| `auto` | 遵循上游 `enabled` 和 `skip_model_request`，明确要求验证时强制取票 |
| `always` | 场景启用时，每次推理前取票 |

推理票据失效且尚未输出时，在原出口重新验证一次。SDK 缺依赖、初始化错误、超时、未知拒绝或人工验证要求不会剔除代理节点；明确的资源连接失败可触发推理代理切换。

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
      "timeout_ms": 30000
    }
  }
}
```

`jsdom_path` 留空使用 `--root` 下的 `captcha_node`，也可指定包含 `node_modules/jsdom` 的目录。设置页自动检测 Node.js 与 jsdom，检测不访问上游或更改配置。

`simulate_canvas_webgl` 为 jsdom 补充 Canvas、WebGL 等模拟接口；可关闭以对照验证结果。模拟环境不保证通过上游判断。无需 Chrome；旧 `backend`、`browser_path` 配置会被忽略。

SDK 支持直连或无认证 HTTP/HTTPS 代理；带认证的代理可通过本机 Mihomo 入口使用。票据仅通过内存、浏览器请求或子进程管道传递，不写入日志或数据库，也不跨请求复用。

### Docker

需要后台验证时加入 `CAPTCHA_RUNTIME=1`；需要订阅代理时加入 `PROXY_RUNTIME=1`。两者默认均为 `0`：

```sh
docker compose build --build-arg CAPTCHA_RUNTIME=1 --build-arg PROXY_RUNTIME=1
docker compose up -d
```

## 故障排查

- **HTTP 405**：`upstream_waf_blocked` 表示上游安全网关拦截，查看错误码、请求 ID 和追踪 ID；仅凭 405 无法判定账号或请求方法有误。
- **领卡 3007**：本次领取未通过安全验证，可在领卡页面重试。
- **SDK 不可用**：检查设置页的 Node.js、jsdom 路径与版本。
- **需要人工验证**：按浏览器组件提示完成，或到 ZCode 客户端领取后刷新余额。

接口依据[阿里云客户端文档](https://help.aliyun.com/zh/captcha/captcha2-0/user-guide/new-architecture-for-web-and-h5-client-access)和[一次性票据约束](https://help.aliyun.com/zh/captcha/captcha2-0/user-guide/server-access)实现；参考项目：[liu5269/zcode2api](https://github.com/liu5269/zcode2api)。
