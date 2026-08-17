<!-- komari-version-hash: __VERSION_HASH__ -->

# Komari Lite 2.2.3 快照

本快照基于 Komari 2.2.3 正式版。快照可能继续调整，关键生产环境升级前请先备份数据库和配置文件。

### 通知默认配置

- 离线通知、流量报告和丢包通知打开「默认配置」时直接显示表单，不再先出现加载转圈再切换。

## 快照信息

- 快照发布时间：__RELEASE_TIME__（北京时间）
- Komari 构建号：`__VERSION_HASH__`
- Komari 与 Komari Web 快照版本：`__APP_VERSION__`

### Docker

```bash
docker pull ghcr.io/nuomiiiii/komari:snapshot
```

镜像包含 `linux/amd64` 和 `linux/arm64`。

升级前请先备份数据库和配置文件。
