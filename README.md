<div align="center">

<img src="https://raw.githubusercontent.com/do-whilefor/zcode2api/main/src/web/favicon.svg" alt="zcode2api" width="80" height="80">

# zcode2api

**ZCode(智谱、Zai）毒鸡蛋反代，让免费账号的重试、领卡和多账号管理交给网关。**

[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-supported-2496ED?logo=docker&logoColor=white)](#快速开始)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20macOS%20%7C%20Linux-555)](#快速开始)
[![License](https://img.shields.io/badge/License-Apache_2.0-orange?logo=apache&logoColor=white)](LICENSE)

[快速开始](#快速开始) · [接入客户端](#接入客户端) · [配置说明](docs/configuration.md) · [更新记录](CHANGELOG.md)

</div>

兼容 OpenAI Chat Completions、Responses 和 Anthropic Messages 协议。

- **账号管理**：BigModel / Z.ai、本机导入、OAuth、分组与会话亲和。
- **调度与重试**：自动换号、并发排队、限流重试；单账号最多 3 个并发请求。
- **自动领卡**：默认每 240 分钟巡检，支持验证取票、领取记录与待生效额度。详见[领卡行为](docs/configuration.md#领卡行为)。
- **网络配置**：HTTP/HTTPS 代理、节点订阅、Mihomo 与安全验证。

## 快速开始

支持 Windows、Linux、macOS。源码构建需要 **Go 1.26+**，Release 可直接运行。后台安全验证需要 Node.js 18+ 和 jsdom。

### Release

从 [Releases](https://github.com/do-whilefor/zcode2api/releases) 下载对应系统与架构的压缩包，完整解压，在解压目录运行。

Windows PowerShell（后台启动，停止用 `.\scripts\stop.ps1`）：

```powershell
.\scripts\start.ps1
```

Linux / macOS（前台运行，按 `Ctrl+C` 停止）：

```bash
chmod +x ./zcode2api
./zcode2api --root .
```

### 源码

下载源码并进入项目根目录。Windows PowerShell 运行 `.\scripts\start.ps1`，脚本会构建并在后台启动；Linux / macOS / WSL 运行：

```bash
go build -trimpath -o bin/zcode2api .
./bin/zcode2api --root .
```

### Docker Compose

在源码根目录运行：

```bash
docker compose up -d --build
```

数据持久化到宿主机 `./data`，端口仅映射到宿主机回环地址。停止并移除容器用 `docker compose down`，数据目录会保留。

### 首次登录

| 用途 | 默认地址 / 文件 |
| --- | --- |
| Web 控制台 | <http://127.0.0.1:6789> |
| 推理网关 | <http://127.0.0.1:6780> |
| 首次管理密码 | `data/admin-password.txt` |
| 实际端口与进程信息 | `data/ports.json` |

设置 `ZCODE2API_DATA_DIR` 后，上述文件位于指定的数据目录。Windows 可用 `Get-Content .\data\admin-password.txt` 读取默认密码文件。

## 接入客户端

1. 登录控制台，在「账号」中通过本机导入或 OAuth 添加账号。本机导入读取**服务所在机器**的 `~/.zcode/v2/credentials.json`，Docker 不会自动读取宿主机凭据。
2. 确认账号状态、模型能力和余额；按需设置分组，在「密钥」中创建网关密钥。
3. 将网关密钥和下表对应的 Base URL 填入客户端。

| 协议 | Base URL | 请求端点 |
| --- | --- | --- |
| OpenAI Chat Completions | `http://127.0.0.1:6780/v1` | `POST /v1/chat/completions` |
| OpenAI Responses | `http://127.0.0.1:6780/v1` | `POST /v1/responses` |
| Anthropic Messages | `http://127.0.0.1:6780` | `POST /v1/messages` |

使用控制台创建的网关密钥。模型列表见 `GET /v1/models`；默认别名为 `glm-flash` → `GLM-5.3-Flash`、`glm-5.3` → `GLM-5.3`。

## 常用配置

优先在控制台「设置」修改，保存后持久化并对后续请求生效。配置文件可参考 [config.example.json](config.example.json)；已保存的控制台设置会覆盖同名文件配置，监听地址和端口变更需要重启。Docker 使用配置文件时需另行挂载到 `/app/config.json`。

| 配置项 | 默认值 | 用途 |
| --- | --- | --- |
| `host` | `127.0.0.1` | 本机监听地址 |
| `web_port` / `gateway_port` | `6789` / `6780` | 控制台 / 网关端口 |
| `claim_auto_enabled` / `claim_auto_min` | `true` / `240` | 自动领卡开关 / 周期（分钟） |
| `http.captcha.mode` | `off` | 推理请求的验证策略 |
| `outbound_proxy.enabled` | `false` | 全局出站代理 |

常用环境变量：`ZCODE2API_DATA_DIR`、`ZCODE2API_HOST`、`ZCODE2API_WEB_PORT`、`ZCODE2API_GATEWAY_PORT`、`ZCODE2API_ADMIN_PASSWORD`。更多配置、重试规则和环境变量见[配置说明](docs/configuration.md)。

订阅代理需要 Mihomo；后台验证需要 Node.js 18+ 和 jsdom。安装方式、Docker 构建参数与故障排查见[网络配置](docs/network.md)。

## 文档与许可

- [更新记录](CHANGELOG.md)
- [配置与重试说明](docs/configuration.md)
- [代理、订阅与上游安全验证](docs/network.md)
- [安全说明与免责声明](SECURITY.md)

采用 [Apache License 2.0](LICENSE)。账号授权、第三方服务条款与部署注意事项见[安全说明](SECURITY.md)。
