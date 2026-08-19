<!-- komari-version-hash: __VERSION_HASH__ -->

本快照基于 Komari 2.2.3 正式版。快照可能继续调整，关键生产环境升级前请先备份数据库和配置文件。

### 新增服务器自动排序

- 新服务器第一次上报并识别出国家/地区后，会排到同一分组、同一国家的最后一台后面。所有服务器都没有分组时，也会按国家排序。没有同组同国家节点时，保持创建时的位置。
- 系统设置 → 通用增加开关，默认关闭。开启后不会修改已有服务器排序。
- 该功能参考并使用了 https://github.com/sanrokamlan-prog 提交的 pull：https://github.com/nuomiiiii/komari/pull/1

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
