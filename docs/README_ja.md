
# Komari 
![Badge](https://hitscounter.dev/api/hit?url=https%3A%2F%2Fgithub.com%2Fkomari-monitor%2Fkomari&label=&icon=github&color=%23a370f7&message=&style=flat&tz=UTC)

![komari](https://socialify.git.ci/komari-monitor/komari/image?description=1&font=Inter&forks=1&issues=1&language=1&logo=https%3A%2F%2Fraw.githubusercontent.com%2Fkomari-monitor%2Fkomari-web%2Fd54ce1288df41ead08aa19f8700186e68028a889%2Fpublic%2Ffavicon.png&name=1&owner=1&pattern=Plus&pulls=1&stargazers=1&theme=Auto)

Komariは、サーバーのパフォーマンスを監視するためのシンプルで効率的なソリューションを提供することを目的とした、軽量の自己ホスト型サーバー監視ツールです。Webインターフェースを介してサーバーのステータスを表示し、軽量エージェントを介してデータを収集します。

[ドキュメント](https://komari-document.pages.dev/) | [Telegramグループ](https://t.me/komari_monitor)

## 特徴
- **軽量で効率的**: リソース消費が少なく、あらゆる規模のサーバーに適しています。
- **自己ホスト型**: データプライバシーを完全に制御でき、展開も簡単です。
- **Webインターフェース**: 直感的な監視ダッシュボードで、使いやすいです。

## クイックスタート

### 0. クラウドホスティングによるワンクリック展開
- Rainyun - CNY 4.5/月 

[![](https://rainyun-apps.cn-nb1.rains3.com/materials/deploy-on-rainyun-cn.svg)](https://app.rainyun.com/apps/rca/store/6780/NzYxNzAz_) 

- Claw Run - USD 1.5/月

[![](https://raw.githubusercontent.com/ClawCloud/Run-Template/refs/heads/main/Run-on-ClawCloud.svg)](https://template.run.claw.cloud/?openapp=system-fastdeploy%3FtemplateName%3Dkomari)

### 1. ワンクリックインストールスクリプトを使用する
systemdを使用するディストリビューション（Ubuntu、Debianなど）に適しています。
```bash
curl -fsSL https://raw.githubusercontent.com/komari-monitor/komari/main/install-komari.sh -o install-komari.sh
chmod +x install-komari.sh
sudo ./install-komari.sh
```

### 2. Docker展開
1. データディレクトリを作成します:
   ```bash
   mkdir -p ./data
   ```
2. Dockerコンテナを実行します:
   ```bash
   docker run -d \
     -p 25774:25774 \
     -v $(pwd)/data:/app/data \
     --name komari \
     ghcr.io/komari-monitor/komari:latest
   ```
3. デフォルトのユーザー名とパスワードを表示します:
   ```bash
   docker logs komari
   ```
4. ブラウザで `http://<your_server_ip>:25774` にアクセスします。

> [!NOTE]
> 環境変数 `ADMIN_USERNAME` と `ADMIN_PASSWORD` を使用して、初期のユーザー名とパスワードをカスタマイズすることもできます。

### 3. バイナリファイル展開
1. Komariの[GitHubリリース](https://github.com/komari-monitor/komari/releases)ページにアクセスして、お使いのオペレーティングシステム用の最新のバイナリをダウンロードします。
2. Komariを実行します:
   ```bash
   ./komari server -l 0.0.0.0:25774
   ```
3. ブラウザで `http://<your_server_ip>:25774` にアクセスします。デフォルトのポートは `25774` です。
4. デフォルトのユーザー名とパスワードは、起動ログで確認するか、環境変数 `ADMIN_USERNAME` と `ADMIN_PASSWORD` を介して設定できます。

> [!NOTE]
> バイナリに実行権限があることを確認してください（`chmod +x komari`）。データは実行ディレクトリの `data` フォルダに保存されます。


### 手動ビルド

#### 依存関係

- Go 1.18+ および Node.js 20+（手動ビルド用）

1. フロントエンドの静的ファイルをビルドします:
   ```bash
   git clone https://github.com/komari-monitor/komari-web
   cd komari-web
   npm install
   npm run build
   ```
2. バックエンドをビルドします:
   ```bash
   git clone https://github.com/komari-monitor/komari
   cd komari
   ```
   ステップ1で生成された静的ファイルを `komari` プロジェクトのルートにある `/public/dist` フォルダにコピーします。
   ```bash 
   go build -o komari
   ```
4. 実行:
   ```bash
   ./komari server -l 0.0.0.0:25774
   ```
   デフォルトのリスニングポートは `25774` です。`http://localhost:25774` にアクセスします。

## フロントエンド開発ガイド
[Komariテーマ開発ガイド | Komari](https://komari-document.pages.dev/dev/theme.html)

[CrowdinでKomariを翻訳する](https://crowdin.com/project/komari/invite?h=cd051bf172c9a9f7f1360e87ffb521692507706)

## クライアントエージェント開発ガイド
[Komariエージェント情報レポートおよびイベント処理ドキュメント](https://komari-document.pages.dev/dev/agent.html)

## 貢献
IssueやPull Requestを歓迎します！

## 謝辞
 - こんなに暇でいられる自分に感謝
 - PRを提出し、テーマを作成してくれたすべての開発者

## Star履歴

[![Star History Chart](https://api.star-history.com/svg?repos=komari-monitor/komari&type=Date)](https://www.star-history.com/#komari-monitor/komari&Date)
