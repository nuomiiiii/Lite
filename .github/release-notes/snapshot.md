<!-- lite-version-hash: __VERSION_HASH__ -->

本快照基于 Lite 2.3.0 正式版。快照可能继续调整，关键生产环境升级前请先备份数据库和配置文件。

## 本次变更

- 管理后台打开更快。仪表盘数字和图表仍会一起出来，更新检查放到空闲后再拉。仪表盘刷新频率和节点实时状态间隔都不改。

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
