<div align="center">

<img src="https://raw.githubusercontent.com/do-whilefor/zcode2api/main/src/web/favicon.svg" alt="zcode2api" width="80" height="80">

# zcode2api

简体中文 · [English](README.en.md) · [日本語](README.ja.md)

**ZCode(智谱、Zai）毒鸡蛋反代，让免费账号的重试、领卡和多账号管理交给网关。**

[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-supported-2496ED?logo=docker&logoColor=white)](#快速开始)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20macOS%20%7C%20Linux-555)](#快速开始)
[![License](https://img.shields.io/badge/License-Apache_2.0-orange?logo=apache&logoColor=white)](LICENSE)

[快速开始](#快速开始) · [接入客户端](#接入客户端) · [配置说明](docs/configuration.md) · [更新记录](CHANGELOG.md)

</div>

面向 ZCode 免费账号的反向代理网关，兼容 OpenAI Chat Completions、Responses 和 Anthropic Messages 协议。

针对阿里云无痕验证 SDK 的接入与风控问题，提供一次性票据隔离、按需预取与失效补充、服务端出口一致性、错误分类和冷却重试。支持多账号调度、自动领卡与官方交互验证兜底，验证结果由上游决定。

## 快速开始

### 源码运行

需要 Git 和 Go 1.26+。

```sh
git clone https://github.com/do-whilefor/zcode2api.git
cd zcode2api
go run . --root .
```

### 可执行文件

从 [Releases](https://github.com/do-whilefor/zcode2api/releases) 下载对应系统和架构的完整压缩包，解压后在其根目录运行，无需安装 Go。

Windows（PowerShell）：

```powershell
.\zcode2api.exe --root .
```

Linux / macOS：

```sh
chmod +x ./zcode2api
./zcode2api --root .
```

### Windows 脚本

在源码或完整 Windows 发行包的根目录运行，后台启动；源码部署需要 Go 1.26+，脚本会自动编译。

```powershell
.\scripts\start.ps1
```

停止时运行 `.\scripts\stop.ps1`。

### Docker Compose

需要 Git 和 Docker Compose。以下构建包含自动验证所需的 Node.js 与 jsdom。

```sh
git clone https://github.com/do-whilefor/zcode2api.git
cd zcode2api
docker compose build --build-arg CAPTCHA_RUNTIME=1
docker compose up -d
```

### 访问与配置

源码、可执行文件和 Windows 脚本部署若需自动验证，另需安装 Node.js 18+，并在项目或解压目录执行：

```sh
npm --prefix captcha_node ci --omit=dev --ignore-scripts
```

打开控制台 [http://127.0.0.1:6789](http://127.0.0.1:6789)，默认管理密码见 `data/admin-password.txt`。账号、设置和日志保存在 `data/`，请保留该目录。

本地部署可复制 `config.example.json` 为 `config.json` 后修改配置；容器配置见[配置说明](docs/configuration.md)。

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

## 文档

[配置说明](docs/configuration.md) · [代理与安全验证](docs/network.md) · [更新记录](CHANGELOG.md) · [安全说明](SECURITY.md) · [Apache 2.0](LICENSE)

本项目为非官方项目，与 ZCode、智谱、Z.ai 无隶属关系。
