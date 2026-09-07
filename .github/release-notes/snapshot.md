<!-- lite-version-hash: __VERSION_HASH__ -->

快照可能继续调整，关键生产环境升级前请先备份数据库和配置文件。

## 主要更新

- iPhone 上除明确的原生 Safari 普通标签页外，管理后台会稳定让开系统状态栏，包括未知浏览器、App 内置页，以及从 Safari 添加到主屏幕。主屏幕即使暂时没报 standalone，只要系统给出顶部安全区也会让开。原生 Safari 标签页不加重复留白；滑动收起浏览器工具栏时顶部留白不再来回切换。iPad 与电脑端相同。
- 一次性费用、流量重置、更换 IP 允许金额为 0；录入失败原因按当前语言显示。
- 节点列表地区筛选补上澳门。
- 延迟监测长目标地址不再挡住间隔列。
- 成本中心按地区筛选时不再白屏。
- 内置 Lite-Theme 回退到改 iOS 安全区之前的 v1.1.1，随本次快照打包，不单独发主题版。

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
