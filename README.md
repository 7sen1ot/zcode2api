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

将自己授权的账号加入账号池，通过统一地址和网关密钥接入支持 OpenAI Chat Completions、Responses 或 Anthropic Messages 的客户端。

- **多账号调度**：支持 BigModel / Z.ai、本机凭据导入、OAuth、分组和会话亲和；单账号最多同时处理 3 个推理请求，所有模型共享名额。
- **自动换号与重试**：账号并发满时换号，全池暂忙则共享排队至恢复或取消；普通限流和过载保留 10 秒重试窗口，已输出内容后停止整轮重放。
- **自动领卡**：合并活动卡与旧版额度卡，支持手动领取、领取记录和待生效额度展示。
- **可选网络配置**：支持出站代理、节点订阅、Mihomo 安装及上游安全验证。

## 快速开始

支持 Windows、Linux、macOS。使用 Release 无需 Go；源码构建需要 **Go 1.26+**。Node.js / jsdom 仅用于可选安全验证和相关测试，普通网关运行无需安装。

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

**网关密钥由本项目签发，不是账号 JWT 或控制台管理密码。** 模型 ID 以 `GET /v1/models` 返回为准；默认别名 `glm-flash` → `GLM-5.3-Flash`、`glm-5.3` → `GLM-5.3`，可用性取决于账号权益和密钥权限。

「领卡」可查看、领取可用卡及查询记录。自动领卡默认每 **240 分钟**检查一次，服务需保持运行；奖励任务仍需在 ZCode 中完成，待生效额度不计入当前余额。网页 Cookie 导入仅用于受支持的平台资料等接口，推理、余额和领卡需要有效的 ZCode 凭据。

## 常用配置

优先在控制台「设置」修改，保存后持久化并对后续请求生效。配置文件可参考 [config.example.json](config.example.json)；已保存的控制台设置会覆盖同名文件配置，监听地址和端口变更需要重启。Docker 使用配置文件时需另行挂载到 `/app/config.json`。

| 配置项 | 默认值 | 用途 |
| --- | --- | --- |
| `host` | `127.0.0.1` | 本机监听地址 |
| `web_port` / `gateway_port` | `6789` / `6780` | 控制台 / 网关端口 |
| `claim_auto_enabled` / `claim_auto_min` | `true` / `240` | 自动领卡开关 / 周期（分钟） |
| `http.captcha.mode` | `off` | 可选上游安全验证 |
| `outbound_proxy.enabled` | `false` | 全局出站代理 |

常用环境变量：`ZCODE2API_DATA_DIR`、`ZCODE2API_HOST`、`ZCODE2API_WEB_PORT`、`ZCODE2API_GATEWAY_PORT`、`ZCODE2API_ADMIN_PASSWORD`。更多配置、重试规则和环境变量见[配置说明](docs/configuration.md)。

代理与安全验证在「设置」中启用，订阅模式需要 Mihomo，安全验证需要 Node.js 18+ 和 jsdom。依赖安装、Docker 可选构建参数、代理轮换和 HTTP 405 排查见[代理与上游安全验证](docs/network.md)。验证环境不保证通过上游风险判定。

## 文档与许可

- [更新记录](CHANGELOG.md)
- [配置与重试说明](docs/configuration.md)
- [代理、订阅与上游安全验证](docs/network.md)
- [安全说明与免责声明](SECURITY.md)

本项目采用 [Apache License 2.0](LICENSE)。再分发须提供许可证副本、显著标明修改，并保留适用的版权与署名声明；具体要求见许可证第 4 条。第三方账号、额度和模型服务仍受各自条款约束。仅接入本人拥有或已获明确授权的账号；远程部署请配置访问控制与 HTTPS。

本项目与 ZCode、智谱、Z.ai、BigModel 官方无隶属或背书关系。
