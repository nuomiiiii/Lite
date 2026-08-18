<!-- komari-version-hash: __VERSION_HASH__ -->

# Komari Lite 2.2.3 快照

本快照基于 Komari 2.2.3 正式版。快照可能继续调整，关键生产环境升级前请先备份数据库和配置文件。

### Agent 在线状态

- 修复 Agent 进程仍在运行、面板却显示离线的问题。WebSocket 断开后会按连接心跳维持在线判定。
- 采集间隔较长，以及未开启探测或探测为 ICMP/TCP/HTTP 时，在线状态保持正常。
- 建议节点同时升级到 Agent `2.2.0.2`，断开后才能自动重连。

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
