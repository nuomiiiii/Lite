# Embedded Web Resources / 内嵌 Web 资源

Lite packages three independent resource groups:

- `systemUI`: administration, terminal, installation, recovery, upgrade, and management pages built from `Lite-web`.
- `bundledThemes`: bundled public dashboard themes copied into `data/theme`. Lite defaults to Lite-Theme. Other themes are installed independently.
- `rescueTheme`: a hidden Lite-Theme fallback used only when the selected public theme is unavailable.

Public themes never provide system application pages or `/system-assets/*`. Custom Head and Body HTML is injected only into public dashboard documents.

Lite 内嵌三组相互独立的资源：

- `systemUI`：由 `Lite-web` 构建的后台、终端、安装、恢复、升级和管理界面。
- `bundledThemes`：安装到 `data/theme` 的内置大屏主题。Lite 默认使用 Lite-Theme。其他主题独立安装。
- `rescueTheme`：当前大屏主题不可用时使用的隐藏 Lite-Theme 保底资源。

大屏主题不能提供系统页面或 `/system-assets/*`。自定义 Head、Body HTML 也只会注入公开大屏页面。

## Build System UI / 构建系统 UI

GitHub Release and snapshot builds clone Lite-web and inject `systemUI/dist` in CI. Do not commit that compiled tree.

GitHub 正式版和快照会在工作流里克隆 Lite-web 并写入 `systemUI/dist`，不要把这棵编译目录提交进仓。

Local binaries still need the same copy step:

本地要自己编二进制时，仍按同样步骤拷入：

```bash
cd Lite-web
npm ci
VITE_SYSTEM_UI_BUILD=1 VITE_BASE_URL=/system-assets/ npm run build

rm -rf /path/to/Lite/web/public/systemUI/dist
mkdir -p /path/to/Lite/web/public/systemUI
cp -r dist /path/to/Lite/web/public/systemUI/dist
```

Before building Lite locally, verify these files exist:

```text
web/public/systemUI/dist/index.html
web/public/bundledThemes/Lite-theme/dist/index.html
web/public/rescueTheme/dist/index.html
```
