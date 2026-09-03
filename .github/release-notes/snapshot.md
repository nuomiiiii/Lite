<!-- lite-version-hash: __VERSION_HASH__ -->

本快照基于 Lite 2.3.1 正式版。快照可能继续调整，关键生产环境升级前请先备份数据库和配置文件。

## 本次变更

- 修复服务器概览「每日网络流量」左侧 Y 轴数字被裁切。
- 感谢 [raymao96](https://github.com/raymao96) 对繁体中文翻译的贡献。
- 内置 Lite-Theme 升级到 1.0.7。
- 仪表盘「服务器状态」「存储概览」显示逻辑跟「成本中心」和「流量概览」对齐：大家一起同行/换行展示。
- 仪表盘「成本中心」卡片展示的标题名称「成本中心」改为「今日费用」，仪表盘配置里的卡片名称保持不变。

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
