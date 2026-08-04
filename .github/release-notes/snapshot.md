# Komari 快照版 __APP_VERSION__

本快照用于验证指标存储、告警查询和 Ping Agent 排序相关改进。建议升级前备份完整的 `data` 目录，并先在测试环境验证。

## 发布信息

- 发布时间：__RELEASE_TIME__（北京时间）
- 内置版本：`__APP_VERSION__`
- Komari 构建号：`__VERSION_HASH__`
- 发布类型：预发布快照，不替代当前正式版

## 上游兼容移植

感谢 [komari-monitor/komari](https://github.com/komari-monitor/komari)。本次基于上游实现思路移植并结合当前分支的数据模型、聚合语义和接口行为完成兼容调整：

- 根据实际写入批次动态分配指标报告队列容量，降低高并发写入时的额外分配。
- 负载告警按单项指标查询，保留当前版本的平均值聚合语义，减少无关指标读取与解码。
- 修正 RAM、Swap 和磁盘容量来源，避免容量字段取值不一致。
- 统一 Ping Agent 排序，并为批量排序增加完整校验；非法或不完整请求不会产生部分更新。

## 本版本 IO 优化

- SQLite 清理在没有过期数据时快速返回，避免无变化场景下的无效事务与后续处理。
- 压缩清理按指标序列判断是否需要处理；没有过期数据的序列跳过解码和重写，降低日常磁盘读写与 CPU 消耗。
- 保留原有完整压缩和保留期扫描频率，不改变数据清理时效，也不额外增加缓存文件或临时数据保留时间。

## 下载

Linux 单文件程序：

- `komari-linux-amd64`
- `komari-linux-arm64`
- `komari-linux-386`
- `komari-linux-riscv64`
- `komari-linux-loong64`

同时提供 `SHA256SUMS` 和 `komari-update.json`，用于校验文件及自动更新识别。

Docker 镜像支持 `linux/amd64` 和 `linux/arm64`：

```bash
docker pull ghcr.io/nuomiiiii/komari:snapshot
```
