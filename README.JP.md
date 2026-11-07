<div align="center">

<img src="https://raw.githubusercontent.com/7sen1ot/zcode2api/main/src/web/favicon.svg" alt="zcode2api" width="80" height="80">

# zcode2api

[简体中文](README.md) · [English](README.EN.md) · 日本語

**ZCode（智谱・Zai）「毒鸡蛋」リバースプロキシ。無料アカウントのリトライ、カード取得、複数アカウント管理をゲートウェイに任せられます。**

[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-supported-2496ED?logo=docker&logoColor=white)](#クイックスタート)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20macOS%20%7C%20Linux-555)](#クイックスタート)
[![License](https://img.shields.io/badge/License-Apache_2.0-orange?logo=apache&logoColor=white)](LICENSE)

[スクリーンショット](#スクリーンショット) · [クイックスタート](#クイックスタート) · [クライアント設定](#クライアント設定) · [設定ガイド（中国語）](docs/configuration.md) · [変更履歴（中国語）](CHANGELOG.md)

</div>

ZCode の無料アカウント向けリバースプロキシゲートウェイです。OpenAI Chat Completions、Responses、Anthropic Messages の各プロトコルに対応しています。

Alibaba Cloud の不可視 CAPTCHA SDK の連携とリスク判定時のエラー処理に対応します。使い捨てチケットの分離、必要に応じた事前取得と期限切れ時の補充、サーバーの接続経路の統一、エラー分類とクールダウン付きリトライを実装しています。複数アカウントの振り分け、カードの自動取得、公式の対話型認証への切り替えにも対応します。認証の成否は上流サービスが決定します。

## スクリーンショット

<table>
  <tr>
    <td width="50%" align="center"><img src="docs/仪表盘1.png" alt="ダッシュボード1" width="100%"><br>ダッシュボード1</td>
    <td width="50%" align="center"><img src="docs/仪表盘2.png" alt="ダッシュボード2" width="100%"><br>ダッシュボード2</td>
  </tr>
  <tr>
    <td width="50%" align="center"><img src="docs/账号管理.png" alt="アカウント管理" width="100%"><br>アカウント管理</td>
    <td width="50%" align="center"><img src="docs/领卡中心.png" alt="カード取得" width="100%"><br>カード取得</td>
  </tr>
  <tr>
    <td width="50%" align="center"><img src="docs/用量.png" alt="使用量" width="100%"><br>使用量</td>
    <td width="50%" align="center"><img src="docs/日志.png" alt="ログ" width="100%"><br>ログ</td>
  </tr>
  <tr>
    <td width="50%" align="center"><img src="docs/密钥.png" alt="API キー" width="100%"><br>API キー</td>
    <td width="50%" align="center"><img src="docs/设置.png" alt="設定" width="100%"><br>設定</td>
  </tr>
</table>

## クイックスタート

### ソースから実行

Git と Go 1.26+ が必要です。

```sh
git clone https://github.com/7sen1ot/zcode2api.git
cd zcode2api
go run . --root .
```

### ビルド済み実行ファイル

[Releases](https://github.com/7sen1ot/zcode2api/releases) から OS とアーキテクチャに合う完全なアーカイブをダウンロードし、展開先のルートで実行します。Go のインストールは不要です。

Windows（PowerShell）：

```powershell
.\zcode2api.exe --root .
```

Linux / macOS：

```sh
chmod +x ./zcode2api
./zcode2api --root .
```

### Windows スクリプト

ソースまたは完全な Windows リリースのルートで実行すると、バックグラウンドで起動します。ソースからの起動には Go 1.26+ が必要で、スクリプトが自動ビルドします。

```powershell
.\scripts\start.ps1
```

停止するには `.\scripts\stop.ps1` を実行します。

### Docker Compose

Git と Docker Compose が必要です。以下のビルドには、CAPTCHA の自動検証に必要な Node.js と jsdom が含まれます。

```sh
git clone https://github.com/7sen1ot/zcode2api.git
cd zcode2api
docker compose build --build-arg CAPTCHA_RUNTIME=1
docker compose up -d
```

### アクセスと設定

ソース、実行ファイル、Windows スクリプトで CAPTCHA の自動検証を利用する場合は、別途 Node.js 18+ をインストールし、プロジェクトまたは展開先のルートで実行してください。

```sh
npm --prefix captcha_node ci --omit=dev --ignore-scripts
```

管理画面 [http://127.0.0.1:6789](http://127.0.0.1:6789) を開きます。初期管理パスワードは `data/admin-password.txt` に保存されます。アカウント、設定、ログを保存する `data/` ディレクトリは保持してください。

ローカルでは `config.example.json` を `config.json` にコピーして設定を変更できます。コンテナの設定は[設定ガイド（中国語）](docs/configuration.md)を参照してください。

## クライアント設定

1. 管理画面でアカウントを追加し、OAuth 認証を完了します。
2. 「密钥」でゲートウェイの API キーを作成します。
3. クライアントに以下を設定します。

| 項目 | 値 |
| --- | --- |
| OpenAI Base URL | `http://127.0.0.1:6780/v1` |
| Anthropic Base URL | `http://127.0.0.1:6780` |
| API キー | 管理画面で作成したゲートウェイのキー |
| モデル | 例：`glm-flash`。利用可能なモデルは `/v1/models` を参照 |

リクエスト例。`YOUR_API_KEY` をゲートウェイのキーに置き換えてください。

```sh
curl http://127.0.0.1:6780/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"glm-flash","messages":[{"role":"user","content":"こんにちは"}]}'
```

## ドキュメント

[設定（中国語）](docs/configuration.md) · [プロキシと検証（中国語）](docs/network.md) · [変更履歴（中国語）](CHANGELOG.md) · [セキュリティ（中国語）](SECURITY.md) · [Apache 2.0](LICENSE)

本プロジェクトは非公式であり、ZCode、智谱、Z.ai とは関係ありません。
