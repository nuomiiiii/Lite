<!-- lite-version-hash: __VERSION_HASH__ -->

本快照基于 Lite 2.3.1 正式版。快照可能继续调整，关键生产环境升级前请先备份数据库和配置文件。

## 本次变更

- iOS 18 加到主屏幕后，管理后台和大屏顶栏会让开系统状态栏，不再和时钟叠在一起。大屏顶栏仍是半透明毛玻璃。
- 加到主屏幕使用 PNG 图标；自己上传的站点图标会同时用于标签页和主屏幕。建议用方图 PNG。
- 内置 Lite-Theme 升级到 1.0.5。
- 管理后台账单标签过长时自动换行叠放。
- 收起侧栏时，子菜单改为同一处弹出。

## 快照信息

- 快照发布时间：__RELEASE_TIME__（北京时间）
- Lite 构建号：`__VERSION_HASH__`
- Lite 与 Lite Web 快照版本：`__APP_VERSION__`

### Docker

```bash
docker pull __DOCKER_IMAGE__
```

镜像包含 `linux/amd64` 和 `linux/arm64`。

升级前请先备份数据库和配置文件。
