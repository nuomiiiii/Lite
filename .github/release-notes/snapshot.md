<!-- lite-version-hash: __VERSION_HASH__ -->

快照可能继续调整，关键生产环境升级前请先备份数据库和配置文件。

## 主要更新

- 一次性费用、流量重置、更换 IP 允许金额为 0；录入失败原因按当前语言显示。
- 节点列表地区筛选补上澳门。
- 延迟监测长目标地址不再挡住间隔列。
- 成本中心按地区筛选时不再白屏。
- 管理后台添加到主屏幕后，页面不再铺进系统状态栏。
- 公开大屏切换深浅色时，系统状态栏会跟着变。

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
