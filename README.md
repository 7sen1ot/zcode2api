<div align="center">

<img src="https://raw.githubusercontent.com/7sen1ot/zcode2api/main/src/web/favicon.svg" alt="zcode2api" width="80" height="80">

# zcode2api

简体中文 · [English](README.EN.md) · [日本語](README.JP.md)

**ZCode(智谱、Zai）毒鸡蛋反代，让免费账号的重试、领卡和多账号管理交给网关。**

[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-supported-2496ED?logo=docker&logoColor=white)](#快速开始)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20macOS%20%7C%20Linux-555)](#快速开始)
[![License](https://img.shields.io/badge/License-Apache_2.0-orange?logo=apache&logoColor=white)](LICENSE)

[界面预览](#界面预览) · [快速开始](#快速开始) · [接入客户端](#接入客户端) · [配置说明](docs/configuration.md) · [更新记录](CHANGELOG.md) · [免责声明](SECURITY.md)

</div>

面向 ZCode 免费账号的反向代理网关，兼容 OpenAI Chat Completions、Responses 和 Anthropic Messages 协议（推荐）。

针对阿里云无痕验证 SDK 的接入与风控问题，提供一次性票据隔离、按需预取与失效补充、服务端出口一致性、错误分类和冷却重试。支持多账号调度、自动领卡与官方交互验证兜底，验证结果由上游决定。

## 界面预览

<table>
  <tr>
    <td width="50%" align="center"><img src="docs/仪表盘1.png" alt="仪表盘1" width="100%"><br>仪表盘1</td>
    <td width="50%" align="center"><img src="docs/仪表盘2.png" alt="仪表盘2" width="100%"><br>仪表盘2</td>
  </tr>
  <tr>
    <td width="50%" align="center"><img src="docs/账号管理.png" alt="账号管理" width="100%"><br>账号管理</td>
    <td width="50%" align="center"><img src="docs/领卡中心.png" alt="领卡中心" width="100%"><br>领卡中心</td>
  </tr>
  <tr>
    <td width="50%" align="center"><img src="docs/用量.png" alt="用量" width="100%"><br>用量</td>
    <td width="50%" align="center"><img src="docs/日志.png" alt="日志" width="100%"><br>日志</td>
  </tr>
  <tr>
    <td width="50%" align="center"><img src="docs/密钥.png" alt="密钥" width="100%"><br>密钥</td>
    <td width="50%" align="center"><img src="docs/设置.png" alt="设置" width="100%"><br>设置</td>
  </tr>
</table>

## 快速开始

### 可执行文件

从 [Releases](https://github.com/7sen1ot/zcode2api/releases) 下载对应系统和架构的完整压缩包，解压后在其根目录运行，无需安装 Go。

Windows（PowerShell）：

```powershell
.\zcode2api.exe --root .
```

Linux / macOS：

```sh
chmod +x ./zcode2api
./zcode2api --root .
```

### 源码运行

需要 Git 和 Go 1.26+。

```sh
git clone https://github.com/7sen1ot/zcode2api.git
cd zcode2api
go run . --root .
```

Windows 源码或完整发行包也可通过 `.\scripts\start.ps1` 后台启动、`.\scripts\stop.ps1` 停止；源码方式会自动编译。

### Docker Compose

需要 Git 和 Docker Compose。以下构建包含自动验证所需的 Node.js 与 jsdom。

```sh
git clone https://github.com/7sen1ot/zcode2api.git
cd zcode2api
docker compose build --build-arg CAPTCHA_RUNTIME=1
docker compose up -d
```

节点订阅代理需再加 `--build-arg PROXY_RUNTIME=1`，custom 工具的 grammar 校验需加 `--build-arg GRAMMAR_RUNTIME=1`；按需选择。

### 访问与配置

直接运行程序且需要服务端自动验证时，安装 Node.js 18+，并在项目或解压目录安装 jsdom：

```sh
npm --prefix captcha_node ci --omit=dev --ignore-scripts
```

打开控制台 [http://127.0.0.1:6789](http://127.0.0.1:6789)，默认管理密码见 `data/admin-password.txt`。账号、设置和日志保存在 `data/`，请保留该目录。

运行参数可在 Web 中设置，或复制 `config.example.json` 为 `config.json`；Web 保存的设置优先。

## 接入客户端

1. 在控制台「账号」中添加账号并完成 OAuth 授权。
2. 在「密钥」中新建网关 API 密钥。
3. 在客户端填写以下参数：

| 参数 | 值 |
| --- | --- |
| OpenAI Base URL | `http://127.0.0.1:6780/v1` |
| Anthropic Base URL | `http://127.0.0.1:6780` |
| API Key | 控制台生成的网关密钥 |
| 模型 | 例如 `glm-flash`；可用模型见 `/v1/models` |

请求示例，将 `YOUR_API_KEY` 替换为网关密钥：

```sh
curl http://127.0.0.1:6780/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"glm-flash","messages":[{"role":"user","content":"你好"}]}'
```

## SDK 验证

### SDK 验证

服务端通过 Node.js 18+ 与 jsdom 运行官方阿里云 SDK，无需 Chrome；依赖安装见[访问与配置](#访问与配置)。在「设置 → 上游安全验证」选择推理策略并保存：

| 策略 | 行为 |
| --- | --- |
| `off`（默认） | 推理不运行验证 SDK |
| `auto`（推荐）| 遵循上游验证场景的启用与免验证配置，明确要求验证时取票 |
| `always` | 上游启用验证场景时，每次推理前取票 |

启用推理验证后，票据失效且尚未输出时，可在原出口重新验证一次；已经输出正文、思考或工具调用时，不重放整轮请求。

## 日志报错说明

调用记录中的 `error_message` 是中文分类摘要，`error_code` 是具体错误码。

| 错误码 / 提示 | 含义与处理方式 |
| --- | --- |
| `upstream_waf_blocked` | 明确的上游安全网关拦截；暂避当前出口，有可用备用节点时切换代理 |
| `proxy_connection_failed` / `proxy_auth_failed` | 代理连接或认证失败；检查代理地址、服务状态及凭据，使用代理池时将节点短时隔离 |
| `proxy_pool_exhausted` / `proxy_upstream_blocked` | 代理池没有可用节点，或出口仍在上游拦截冷却中；检查节点或等待冷却结束 |
| `3012` / `upstream_risk_blocked` | 按账号风控拒绝处理；本次请求可换其他可用账号，不因此剔除代理或自动禁用账号，也不能据此判定永久封禁 |
| `3008` / `3009` / `3010` | 上游账号并发暂忙；优先使用其他可用账号，没有可用名额时排队等待 |
| `account_busy` | 网关等待可用账号槽位超时；检查账号数量、并发占用和排队时长 |
| `rate_limit_error` / 请求频率受限 | 上游限流；遵守等待时间和重试窗口，不因此判定代理损坏 |
| `authentication_error` / `insufficient_quota` | 分别表示上游账号凭据失效、账号额度不足；需要重新授权或补充可用额度，本次请求可使用其他可用账号 |
| `coding_plan_security_verification_required`，或带明确验证提示的 `3007` | 上游要求安全验证，进入对应的 SDK 或人工验证流程；`3007` 需结合提示判断，不能一律视为验证码问题 |
| `captcha_risk_blocked` / `F001` | SDK 风控拒绝；不据此认定账号或代理失效，也不触发代理节点切换 |
| `F010` / `F011` | SDK 出口 / 设备访问频率受限；分别按对应作用域冷却 |
| `captcha_expired`、`F019`、`F008`、`F018` | 验证票据失效或重复提交；需要新票据，不能复用已提交的结果 |
| `captcha_jsdom_missing` / `captcha_runtime_error` / `captcha_timeout` | SDK 依赖缺失、运行异常或验证超时；检查 Node.js、jsdom、网络及验证配置 |
| `first_token_timeout` / `stream_idle_timeout` / `inference_timeout` | 分别表示首个有效输出超时、流式输出空闲超时、整轮推理超时 |
| `proxy_subscription_auth_failed` | 订阅认证失败，令牌可能错误或已失效；重新复制有效的订阅链接 |

## 详细文档

| 文档 | 内容 |
| --- | --- |
| [配置与重试](docs/configuration.md) | 参数与环境变量、版本更新、额度刷新、定时领卡、调度和日志 |
| [代理与安全验证](docs/network.md) | 节点选择与切换、SDK 依赖、票据管理、人工验证及错误码排查 |
| [协议兼容](docs/protocol-compatibility.md) | 工具调用、grammar 校验、系统提示词去重和指令转换限制 |

## 贡献指南

欢迎通过 **Issue** 反馈问题、提出建议，也欢迎通过 **Pull Request（PR）** 参与项目开发与完善。

**参与贡献：**

1. **Fork** 本仓库，并创建新的开发分支。
2. **提交改动**，确保代码清晰、规范，必要时补充说明。
3. **发起 PR**，描述改动内容及目的，等待审核与合并。

无论是修复 Bug、优化功能，还是完善文档，每一份贡献都值得感谢。

如果本项目对你有所帮助，欢迎点亮一个 **Star**，这也是对项目持续维护与发展的支持！

## 安全与免责

本项目为非官方项目，与 ZCode、智谱、Z.ai、BigModel 无隶属或背书关系。仅接入本人拥有或已获明确授权的账号，并遵守第三方服务条款。

数据目录包含数据库、主密钥、管理密码及可能明文保存的 Cookie，应限制访问并妥善备份。

上游接口、活动、额度和账号限制可能变化，重试与 SDK 验证不保证请求成功。项目按 [Apache License 2.0](LICENSE) 开源，软件按“现状”提供，担保免责与责任限制以许可证第 7、8 条及适用法律为准。完整声明见[免责声明](SECURITY.md)。
