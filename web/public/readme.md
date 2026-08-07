# Embedded Web Resources / 内嵌 Web 资源

Komari packages three independent resource groups:

- `systemUI`: administration, terminal, installation, recovery, upgrade, and management pages built from `nuomiiiii/komari-web`.
- `bundledThemes`: the bundled Nezha public dashboard theme copied into `data/theme` during the one-time migration. Other themes are installed independently.
- `rescueTheme`: a hidden Nezha fallback used only when the selected public theme is unavailable.

Public themes never provide system application pages or `/system-assets/*`. Custom Head and Body HTML is injected only into public dashboard documents.

Komari 内嵌三组相互独立的资源：

- `systemUI`：由 `nuomiiiii/komari-web` 构建的后台、终端、安装、恢复、升级和管理界面。
- `bundledThemes`：首次迁移时安装到 `data/theme` 的内置 Nezha 大屏主题，其他主题独立安装。
- `rescueTheme`：当前大屏主题不可用时使用的隐藏 Nezha 保底资源。

大屏主题不能提供系统页面或 `/system-assets/*`。自定义 Head、Body HTML 也只会注入公开大屏页面。

## Build System UI / 构建系统 UI

```bash
cd komari-web
npm ci
VITE_SYSTEM_UI_BUILD=1 VITE_BASE_URL=/system-assets/ npm run build

rm -rf /path/to/komari/web/public/systemUI
mkdir -p /path/to/komari/web/public/systemUI
cp -r dist /path/to/komari/web/public/systemUI/
```

Before building Komari, verify these files exist:

```text
web/public/systemUI/dist/index.html
web/public/bundledThemes/nezha/dist/index.html
web/public/rescueTheme/dist/index.html
```
