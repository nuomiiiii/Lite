<!-- komari-version-hash: __VERSION_HASH__ -->

# Komari Lite 2.2.2 优化快照

本快照基于 Komari 2.2.2 正式版，包含上游 1.4.x 兼容、系统 UI 加载优化和无效代码清理。快照可能继续调整，关键生产环境升级前请先备份数据库和配置文件。

## 页面切换体验（__UPDATE_TIME__，北京时间）

- 修复后台页面首次切换时出现整页白屏或加载框架闪现的问题。
- 后台顶栏和侧栏在页面代码加载期间保持显示，加载状态仅限内容区。

## 本次更新（2026-08-12 17:24，北京时间）

### 上游 Komari 1.4.x 兼容

- 新增上游 1.4.x `metric_label_sets` 指标数据库布局识别，保留 1.3.1 `metric_labels` 兼容。
- 1.3.1 与 1.4.x 共用 V4 指标迁移流程，历史时间戳从毫秒安全转换为纳秒。
- 同时检测到两套完整上游布局时拒绝迁移，避免误选源表。
- 指标迁移在 SQLite 事务中完成，失败自动回滚且不残留临时表。

### PWA 动态站点信息

- `/manifest.json`、`/manifest.webmanifest` 和 `/system-assets/manifest.json` 实时使用后台设置的站点名称、描述与 `/favicon.ico`。
- 保留公开主题自身的显示模式、作用域和配色；系统 UI 继续使用独立配色。
- 增加标准 JSON 编码和 `no-store`，后台改名后不会继续使用旧缓存。
- 第三方主题缺少 manifest 时自动生成合理默认内容。

### 后台加载优化

- 删除应用根级和后台全局的数据 Provider，改为仅在延迟任务、回程线路和终端等实际使用页面加载相应数据。
- 普通后台页面不再无条件请求节点详情、延迟任务或节点列表。
- 后台页面代码在登录后空闲时预热，并保留后台入口及菜单悬停、聚焦时的按需预加载，减少首次切换等待。
- 构建分析器改为仅在 `ANALYZE=1` 时运行，默认生产构建不再生成 `bundle-analysis.html`。
- 删除无实际用途的 `__BUILD_TIME__` 构建常量。

### 无效代码清理

- 删除两个无功能的旧 PWA 提示组件，保留正常工作的断网提示。
- 删除无引用图标、在线提示、设置按钮、类型、区域辅助函数和未使用的许可证字符串常量。
- 删除后端无引用的 ASN 包装函数、布尔转换函数，以及已经被缓存实现替代的旧仪表盘构建函数。
- 实际使用的线路规则、缓存仪表盘、用户 EULA 和主题兼容逻辑均保留。

### 版本与系统 UI

- Komari 与 Komari Web 版本统一为 `2.2.2`，没有升级或重新解析依赖。
- 本次 Web 正式构建已完整嵌入后端，系统 UI 继续使用 `/system-assets/`。
- 公开大屏仍由当前启用主题提供，未修改 Nezha 或其他第三方主题业务逻辑。

## 发布信息

- 快照版本：`__APP_VERSION__`
- 后端提交：`__VERSION_HASH__`
- Komari Web 提交：`749ea2c`
- 目标正式版本：`2.2.2`
- 发布时间：__RELEASE_TIME__（北京时间）

### Docker

```bash
docker pull ghcr.io/nuomiiiii/komari:snapshot
```

镜像包含 `linux/amd64` 和 `linux/arm64`。

### Linux 一键安装

```bash
curl -fsSL https://raw.githubusercontent.com/nuomiiiii/komari/main/install-komari.sh -o install-komari.sh && chmod +x install-komari.sh && sudo ./install-komari.sh
```

运行安装脚本后，在发布通道中选择“快照版（最新功能）”，脚本会自动安装当前最新的 `Snapshot-*` 版本。

其他 Linux 架构可在本 Release 中下载对应的单文件程序，并使用 `SHA256SUMS` 校验。升级前请先备份数据库和配置文件。
