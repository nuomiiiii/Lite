<!-- lite-version-hash: __VERSION_HASH__ -->

本快照基于 Lite 2.3.1 正式版。快照可能继续调整，关键生产环境升级前请先备份数据库和配置文件。

## 本次变更

- 修复服务器概览「每日网络流量」左侧 Y 轴数字被裁切。最高档实际是 `167.64GB` 这类三位整数，首位被切掉后看起来像 `67.64GB`，会误以为刻度和柱子总量对不上。
- 回程「疑似被墙」实验室功能改为：开启时必须选择辅助延迟任务。
- 管理后台补齐繁体中文、日语、英语文案，覆盖回程监测、服务器概览，以及原先缺键而一直回落到简体中文的按钮、导航和分页。
- 感谢 [raymao96](https://github.com/raymao96) 对繁体中文翻译的贡献。
- 内置 Lite-Theme 升级到 1.0.7。

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
