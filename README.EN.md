<div align="center">

<img src="https://raw.githubusercontent.com/7sen1ot/zcode2api/main/src/web/favicon.svg" alt="zcode2api" width="80" height="80">

# zcode2api

[简体中文](README.md) · English · [日本語](README.JP.md)

**A ZCode (Zhipu / Zai) “毒鸡蛋” reverse proxy. Let the gateway handle retries, card claims and multi-account management for free accounts.**

[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-supported-2496ED?logo=docker&logoColor=white)](#quick-start)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20macOS%20%7C%20Linux-555)](#quick-start)
[![License](https://img.shields.io/badge/License-Apache_2.0-orange?logo=apache&logoColor=white)](LICENSE)

[Screenshots](#screenshots) · [Quick start](#quick-start) · [Client setup](#client-setup) · [Configuration (Chinese)](docs/configuration.md) · [Changelog (Chinese)](CHANGELOG.md) · [Disclaimer (Chinese)](SECURITY.md)

</div>

A reverse proxy gateway for ZCode free accounts, supporting OpenAI Chat Completions, Responses and Anthropic Messages (recommended) protocols.

Handles Alibaba Cloud's invisible CAPTCHA SDK integration and risk-control errors through isolated single-use tickets, on-demand prefetching and replacement of expired tickets, consistent server egress, error classification and retry cooldowns. Includes account scheduling, automatic card claims and fallback to the official interactive verification widget. Verification outcomes are determined by the upstream service.

## Screenshots

<table>
  <tr>
    <td width="50%" align="center"><img src="docs/仪表盘1.png" alt="Dashboard 1" width="100%"><br>Dashboard 1</td>
    <td width="50%" align="center"><img src="docs/仪表盘2.png" alt="Dashboard 2" width="100%"><br>Dashboard 2</td>
  </tr>
  <tr>
    <td width="50%" align="center"><img src="docs/账号管理.png" alt="Accounts" width="100%"><br>Accounts</td>
    <td width="50%" align="center"><img src="docs/领卡中心.png" alt="Card claims" width="100%"><br>Card claims</td>
  </tr>
  <tr>
    <td width="50%" align="center"><img src="docs/用量.png" alt="Usage" width="100%"><br>Usage</td>
    <td width="50%" align="center"><img src="docs/日志.png" alt="Logs" width="100%"><br>Logs</td>
  </tr>
  <tr>
    <td width="50%" align="center"><img src="docs/密钥.png" alt="API keys" width="100%"><br>API keys</td>
    <td width="50%" align="center"><img src="docs/设置.png" alt="Settings" width="100%"><br>Settings</td>
  </tr>
</table>

## Quick start

### Prebuilt binaries

Download the full archive for your OS and architecture from [Releases](https://github.com/7sen1ot/zcode2api/releases), extract it and run from its root directory. Go is not required.

Windows (PowerShell):

```powershell
.\zcode2api.exe --root .
```

Linux / macOS:

```sh
chmod +x ./zcode2api
./zcode2api --root .
```

### Run from source

Requires Git and Go 1.26+.

```sh
git clone https://github.com/7sen1ot/zcode2api.git
cd zcode2api
go run . --root .
```

On Windows, source checkouts and full release packages also support `.\scripts\start.ps1` to start in the background and `.\scripts\stop.ps1` to stop. The source workflow builds the executable automatically.

### Docker Compose

Requires Git and Docker Compose. This build includes Node.js and jsdom for automatic CAPTCHA verification.

```sh
git clone https://github.com/7sen1ot/zcode2api.git
cd zcode2api
docker compose build --build-arg CAPTCHA_RUNTIME=1
docker compose up -d
```

Add `--build-arg PROXY_RUNTIME=1` for subscription-based proxy nodes, or `--build-arg GRAMMAR_RUNTIME=1` for grammar validation of custom tools, as needed.

### Access and configuration

If you run the application directly and need automatic server-side verification, install Node.js 18+ and install jsdom from the project or extracted release root:

```sh
npm --prefix captcha_node ci --omit=dev --ignore-scripts
```

Open the console at [http://127.0.0.1:6789](http://127.0.0.1:6789). The default admin password is in `data/admin-password.txt`. Keep the `data/` directory: it stores accounts, settings and logs.

Configure runtime settings in the web console, or copy `config.example.json` to `config.json`. Settings saved through the web console take precedence.

## Client setup

1. Add an account under **账号** (Accounts) in the console and complete OAuth authorization.
2. Create a gateway API key under **密钥** (Keys).
3. Configure your client:

| Setting | Value |
| --- | --- |
| OpenAI Base URL | `http://127.0.0.1:6780/v1` |
| Anthropic Base URL | `http://127.0.0.1:6780` |
| API key | The gateway key created in the console |
| Model | For example, `glm-flash`; see `/v1/models` for available models |

Example request; replace `YOUR_API_KEY` with your gateway key:

```sh
curl http://127.0.0.1:6780/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"glm-flash","messages":[{"role":"user","content":"Hello"}]}'
```

## SDK verification

### SDK verification

The server runs the official Alibaba Cloud SDK using Node.js 18+ and jsdom; Chrome is not required. For dependency installation, see [Access and configuration](#access-and-configuration). Select an inference verification policy under **设置 → 上游安全验证** (Settings → Upstream security verification), then save:

| Policy | Behavior |
| --- | --- |
| `off` (default) | Do not run the verification SDK for inference |
| `auto` (recommended) | Follow the upstream settings for enabled verification scenes and verification exemptions; obtain a ticket when verification is explicitly required |
| `always` | Obtain a ticket before each inference request when the upstream verification scene is enabled |

When inference verification is enabled, an expired ticket can trigger one fresh verification on the same egress route if no output has been sent. Once response text, reasoning or tool calls have been sent, the full request is not replayed.

## Log errors

In request logs, `error_message` is a categorized summary in Chinese, and `error_code` is the specific error code.

| Error code / message | Meaning and action |
| --- | --- |
| `upstream_waf_blocked` | Explicit upstream security gateway block; put the current egress route on cooldown and switch proxies when a usable backup node is available |
| `proxy_connection_failed` / `proxy_auth_failed` | Proxy connection or authentication failure; check the proxy address, service status and credentials. When using a proxy pool, the node is temporarily quarantined |
| `proxy_pool_exhausted` / `proxy_upstream_blocked` | No usable proxy nodes remain, or the egress route is still on cooldown after an upstream block; check nodes or wait for the cooldown to expire |
| `3012` / `upstream_risk_blocked` | Treated as an account-level risk-control rejection; the request may use another available account. This does not remove the proxy or automatically disable the account, and is not evidence of a permanent ban |
| `3008` / `3009` / `3010` | The upstream account is temporarily at its concurrency limit; prefer another available account, or queue until capacity is available |
| `account_busy` | The gateway timed out waiting for an available account slot; check account count, concurrency usage and queue wait time |
| `rate_limit_error` / request rate limited | Upstream rate limiting; respect the wait time and retry window. This does not indicate a broken proxy |
| `authentication_error` / `insufficient_quota` | Upstream account credentials are invalid, or the account has insufficient quota, respectively; reauthorize or add usable quota. The request may use another available account |
| `coding_plan_security_verification_required`, or `3007` with an explicit verification message | The upstream service requires security verification; use the corresponding SDK or manual verification flow. Interpret `3007` together with its message; it does not always indicate a CAPTCHA issue |
| `captcha_risk_blocked` / `F001` | SDK risk-control rejection; this does not establish that the account or proxy is invalid, and does not trigger a proxy node switch |
| `F010` / `F011` | SDK request rate limited for the egress route or device, respectively; apply cooldown to the corresponding scope |
| `captcha_expired`, `F019`, `F008`, `F018` | Verification ticket expired or submitted more than once; obtain a fresh ticket. A submitted result cannot be reused |
| `captcha_jsdom_missing` / `captcha_runtime_error` / `captcha_timeout` | Missing SDK dependency, runtime error or verification timeout; check Node.js, jsdom, network connectivity and verification settings |
| `first_token_timeout` / `stream_idle_timeout` / `inference_timeout` | Timeout waiting for the first meaningful output, timeout while a stream is idle, or timeout for the entire inference request, respectively |
| `proxy_subscription_auth_failed` | Proxy subscription authentication failed; the token may be incorrect or expired. Copy a valid subscription URL again |

## Detailed documentation

| Document | Contents |
| --- | --- |
| [Configuration and retries (Chinese)](docs/configuration.md) | Parameters and environment variables, version updates, quota refresh, scheduled card claims, scheduling and logs |
| [Proxies and security verification (Chinese)](docs/network.md) | Node selection and failover, SDK dependencies, ticket management, manual verification and error troubleshooting |
| [Protocol compatibility (Chinese)](docs/protocol-compatibility.md) | Tool calls, grammar validation, system prompt deduplication and instruction conversion limitations |

## Contributing

You are welcome to report problems and suggest improvements through **Issues**, or help develop and improve the project through **Pull Requests (PRs)**.

**How to contribute:**

1. **Fork** this repository and create a new development branch.
2. **Commit your changes**, keeping the code clear and consistent with the project's conventions. Add explanations where needed.
3. **Open a PR**, describe your changes and their purpose, and wait for review and merging.

Whether you fix a bug, improve a feature or update the documentation, every contribution is appreciated.

If this project helps you, consider giving it a **Star** to support its continued maintenance and development!

## Security and disclaimer

This is an unofficial project with no affiliation with or endorsement from ZCode, Zhipu, Z.ai or BigModel. Only connect accounts you own or are explicitly authorized to use, and comply with the third-party service terms.

The data directory contains the database, master key, admin password and potentially plaintext cookies. Restrict access and keep secure backups.

Upstream APIs, promotions, quotas and account restrictions may change. Retries and SDK verification do not guarantee successful requests. This project is open source under the [Apache License 2.0](LICENSE) and the software is provided “AS IS”. Warranty disclaimers and limitations of liability are subject to Sections 7 and 8 of the license and applicable law. See the [full disclaimer (Chinese)](SECURITY.md).
