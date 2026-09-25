<!-- lite-version-hash: __VERSION_HASH__ -->

快照可能继续调整，关键生产环境升级前请先备份数据库和配置文件。

## 主要更新

- 修复新建回程监测时，「探测节点」下方出现大片空白的问题。任务名称和探测节点恢复为同一行、同一高度。
- 修复手机端远程终端无法单独发送回车的问题。命令框为空时，点发送也会向终端送出回车。

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
