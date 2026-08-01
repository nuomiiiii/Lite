# Komari

![Komari](docs/assets/branding/komari-banner.svg)

[![Release](https://img.shields.io/github/v/release/nuomiiiii/komari?label=release)](https://github.com/nuomiiiii/komari/releases)
[![Docker](https://img.shields.io/badge/GHCR-nuomiiiii%2Fkomari-2496ED?logo=docker)](https://github.com/nuomiiiii/komari/pkgs/container/komari)
[![License](https://img.shields.io/github/license/nuomiiiii/komari)](LICENSE)

Komari 是一款轻量级、自托管的服务器监控与管理工具。服务端提供 Web 管理界面，Agent 负责采集节点状态、执行延迟与回程线路探测，以及经过授权的远程操作。本分支基于 [komari-monitor/komari](https://github.com/komari-monitor/komari) 持续开发，重点优化低配置设备上的数据库占用、维护负载和日常运维体验。

> [!WARNING]
> Komari 只能部署在你拥有或已获得授权管理的设备上。请勿将其用于未经授权的访问、持久化、命令执行或其他滥用行为。管理员应为面板启用 HTTPS 和双因素认证，并妥善保护 Agent Token 与备份文件。

## 2.1.12 主要更新

- **数据库占用进一步降低**：指标点、延迟与丢包记录采用更紧凑的无损存储，并通过真正的分层接力减少重复数据。同一份已填满 7 天的实测数据库由 21.84 MiB 降至 18.81 MiB，减少约 **13.9%**；实际效果会随节点数量、探测任务和数据波动而变化。
- **大数据库查询更快**：历史页面只读取当前需要的数据，延迟统计由一次查询统一生成，减少长周期、大容量数据下的重复扫描。
- **低性能主控更稳定**：根据可用内存、CPU 核心数和容器资源限制自动调整数据库缓存与查询并发；压缩、数据接力、WAL 维护和空间回收改为分批执行，降低单核、ARM64 和慢速磁盘上的瞬时压力。
- **后台仪表盘**：管理端默认首页集中展示服务器在线状态、离线节点、今日实时上传与下载、今日计费流量、最近一个月每日计费流量和数据库文件组成。
- **迁移过程可见且可恢复**：兼容上游 `1.3.1`、`1.3.2` 以及本分支 `2.1.7` 至 `2.1.11` 的数据库升级；迁移页面会显示阶段、进度和错误，失败时回滚当前操作并保留原数据。
- **远程终端体验改进**：优化多标签、多节点连续连接和残留会话清理，兼容 Cloudflare Tunnel、常见反向代理、两步验证和离线节点场景。
- **后台界面统一**：重新整理导航层级，统一仪表盘、服务器、监测、通知、主题、设置、账户等页面的标题、间距与布局，并同步优化电脑端和手机端。

## 核心功能

- CPU、内存、磁盘、网络、负载、连接数和运行时间监控
- 后台仪表盘集中展示节点状态、实时流量、月度流量和数据库占用
- 节点分组、排序、标签、账单、流量限额与重置日管理
- IPv4/IPv6 延迟监测、历史曲线、任务排序与异常告警
- 电信、移动、联通回程线路监测，支持切线识别、恢复判断、事件记录与通知
- 离线、流量和延迟通知，以及日/周/月流量报告
- 可安装主题与托管主题配置
- 多标签远程终端、文件管理和远程任务执行
- SQLite 数据库占用明细、运行诊断、备份和手动空间回收
- Linux 直接安装、Docker 与多架构二进制部署

## 数据库与升级说明

### SQLite 指标存储

SQLite 指标库使用紧凑的无损编码和分层保留。压缩不会额外改变已采集指标的数值精度，详细指标和历史汇总仍遵循后台设置的保留周期与原有采样/汇总规则；流量报表所需的累计值由独立精确账本保存。

数据库缓存、查询并发和后台维护批次会根据主控资源自动调整。已经形成的历史数据堆积会从保存位置继续处理，每批成功后才清理对应旧数据；失败时保留原数据，后续继续重试。

这套迁移仅适用于使用 SQLite 的指标存储。外置 MySQL 或 PostgreSQL 不会进入 SQLite 指标库迁移，也不会显示对应进度。

### 从旧版本升级

> [!IMPORTANT]
> `2.1.12` 包含指标数据库迁移，建议先备份完整 `data` 目录，再通过手动替换程序或重建 Docker 容器更新。首次启动时不要中断迁移，并等待迁移页面明确显示完成。

1. 停止旧 Komari，备份程序和完整 `data` 目录。
2. 更新程序或容器，并保持原有 `data` 挂载不变。
3. 打开管理页面查看迁移进度，确认服务正常后再删除备份。
4. 升级会自动清理已删除服务器和延迟任务的历史残留。SQLite 产生的空闲页会继续复用；如需立即把空间归还给磁盘，可在业务低峰期手动执行一次“回收空间”。

上游 `1.3.1`、`1.3.2` 以及本分支 `2.1.7` 至 `2.1.11` 的 SQLite 指标库都会通过同一迁移页面转换为当前格式，原有数据保留周期不会被改回默认值。迁移前仍建议备份完整 `data` 目录。

监控数据固定启用分层降采样；升级时保留原有数据保留天数，并自动停用旧版低资源模式。

## 快速开始

### Linux 一键安装

适用于使用 systemd 的常见 Linux 发行版：

```bash
curl -fsSL https://raw.githubusercontent.com/nuomiiiii/komari/main/install-komari.sh -o install-komari.sh
chmod +x install-komari.sh
sudo ./install-komari.sh
```

安装完成后访问 `http://<服务器 IP>:25774`。正式环境建议配置反向代理和 HTTPS。

### Docker

```bash
mkdir -p ./data
docker run -d --name komari --restart unless-stopped -p 25774:25774 -v "$(pwd)/data:/app/data" ghcr.io/nuomiiiii/komari:latest
```

更新 Docker 部署：

```bash
docker pull ghcr.io/nuomiiiii/komari:latest
docker rm -f komari
```

随后使用原来的端口和 `data` 挂载重新创建容器。不要在未确认备份的情况下删除宿主机数据目录。

### 二进制文件

从 [Releases](https://github.com/nuomiiiii/komari/releases) 下载与系统架构匹配的文件：

```bash
chmod +x komari-linux-amd64
./komari-linux-amd64 server -l 0.0.0.0:25774
```

默认数据目录为程序工作目录下的 `data`。

## Agent 与远程管理

Agent 项目与安装说明见 [nuomiiiii/komari-agent](https://github.com/nuomiiiii/komari-agent)。`2.1.x` 服务端建议搭配当前兼容版本 Agent 使用；远程终端、文件管理和任务执行只有在 Agent 支持并由管理员主动发起时才可用。

远程入口受管理员登录、双因素认证和短时会话限制。包括 Komari Server 所在节点在内，已授权并正常在线的 Agent 均可使用 Web 终端、文件和 Docker 管理；这不会修改系统 SSH、防火墙或其他远程连接配置。

## Linux 一键更新与回退

一键更新只面向官方脚本安装、由 systemd 管理且满足运行时检查的 Linux 实例。更新程序会校验版本、构建标识、大小和 SHA-256，备份当前程序及 `data`，并在新进程健康检查失败时恢复旧版本。

Docker、Windows、非 systemd 环境、外置指标数据库和不满足原子回退条件的部署不会启用该入口。详细限制见 [Linux 一键更新与回退](docs/self-update.md)。

## 从源码构建

后端使用 Go `1.25`，前端建议使用 Node.js `20+`：

```bash
git clone https://github.com/nuomiiiii/komari.git
cd komari
go build -o komari
./komari server -l 0.0.0.0:25774
```

正式构建还需要将 [nuomiiiii/komari-web](https://github.com/nuomiiiii/komari-web) 的构建产物放入默认主题目录。可参考仓库内的 GitHub Actions 构建流程。

## 相关链接

- [版本发布](https://github.com/nuomiiiii/komari/releases)
- [Agent](https://github.com/nuomiiiii/komari-agent)
- [Nezha 主题](https://github.com/nuomiiiii/nezha)
- [Komari 文档](https://komari-document.pages.dev/)
- [上游 Komari](https://github.com/komari-monitor/komari)

## 致谢

感谢 Komari 上游维护者、Agent 与主题贡献者，以及持续提供测试和反馈的社区用户。本分支保留上游版权与 MIT 许可证。

## 许可证

[MIT License](LICENSE)
