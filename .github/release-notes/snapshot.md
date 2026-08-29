<!-- lite-version-hash: __VERSION_HASH__ -->

本快照基于 Lite 2.3.1 正式版。快照可能继续调整，关键生产环境升级前请先备份数据库和配置文件。

## 本次变更

- 优化管理后台的加载逻辑，提升加载速度。
- 没有缓存时切换页面或页签，不再先闪空白或骨架。
- 离线通知和流量定时报告会等数据就绪再打开，不再直接闪现。
- 账单中心再次进入时直接显示已加载的数据。
- 远程终端和远程命令的在线状态颜色与服务器列表一致。

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
