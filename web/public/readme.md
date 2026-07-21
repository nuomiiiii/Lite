# Frontend Build Instructions / 前端构建说明 / フロントエンド構築手順

## English

### Frontend Repository

- **Frontend project repository**: https://github.com/nuomiiiii/komari-web

### Build Requirements

1. Clone the frontend repository and build the static files
2. Copy the generated `dist` files to `web/public/defaultTheme/dist` in the backend repository
3. Copy `komari-theme.json` to `web/public/defaultTheme` if you want the default theme metadata and managed configuration to be available
4. Ensure `web/public/defaultTheme/dist/index.html` exists before building the backend

### Important Note

⚠️ **This build follows the `nuomiiiii/komari-web` fork. Keep backend and frontend releases on matching version tags.**

---

## 中文

### 前端项目仓库

- **前端项目地址**: https://github.com/nuomiiiii/komari-web

### 构建要求

1. 克隆前端仓库并构建静态文件
2. 将生成的 `dist` 文件复制到后端仓库内的 `web/public/defaultTheme/dist`
3. 如需让后台显示默认主题元数据和可管理配置，将 `komari-theme.json` 复制到 `web/public/defaultTheme`
4. 构建后端前，确保 `web/public/defaultTheme/dist/index.html` 存在

### 重要提醒

⚠️ **此版本使用 `nuomiiiii/komari-web` 分支，后端与前端应使用相同版本标签。**

---

## 日本語

### フロントエンドプロジェクトリポジトリ

- **フロントエンドプロジェクトアドレス**: https://github.com/nuomiiiii/komari-web

### ビルド要件

1. フロントエンドリポジトリをクローンして静的ファイルをビルドする
2. 生成された `dist` ファイルをバックエンドリポジトリ内の `web/public/defaultTheme/dist` にコピーする
3. デフォルトテーマのメタデータと管理設定を利用する場合は、`komari-theme.json` を `web/public/defaultTheme` にコピーする
4. バックエンドをビルドする前に、`web/public/defaultTheme/dist/index.html` が存在することを確認する

### 重要な注意事項

⚠️ **このビルドは `nuomiiiii/komari-web` フォークを使用します。バックエンドとフロントエンドで同じバージョンタグを使用してください。**

---

## Quick Setup / 快速设置 / クイックセットアップ

```bash
# Clone frontend repository / 克隆前端仓库 / フロントエンドリポジトリをクローン
git clone https://github.com/nuomiiiii/komari-web
cd komari-web

# Install dependencies and build / 安装依赖并构建 / 依存関係をインストールしてビルド
npm install
npm run build

# Copy frontend assets into the backend embed directory / 复制到后端 embed 目录 / バックエンドの embed ディレクトリにコピー
mkdir -p /path/to/komari/web/public/defaultTheme/dist
cp -r dist/* /path/to/komari/web/public/defaultTheme/dist/
cp komari-theme.json /path/to/komari/web/public/defaultTheme/
```
