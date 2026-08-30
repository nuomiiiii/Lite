<!-- lite-version-hash: __VERSION_HASH__ -->

本快照基于 Lite 2.3.1 正式版。快照可能继续调整，关键生产环境升级前请先备份数据库和配置文件。

## 本次变更

- 管理后台和大屏顶栏会让开系统状态栏。
- 管理后台账单标签过长时自动换行叠放。
- 收起侧栏时，子菜单改为同一处弹出。
- 服务器到期设为长期后，成本中心不再计算该服务器的剩余价值；日、月、年账单仍按原价计算。
- 内置 Lite-Theme 升级到 1.0.5。

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
