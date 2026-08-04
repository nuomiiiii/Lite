# Komari Lite

![Komari Lite](docs/assets/branding/komari-banner.svg)

[![Release](https://img.shields.io/github/v/release/nuomiiiii/komari?label=release)](https://github.com/nuomiiiii/komari/releases)
[![Docker](https://img.shields.io/badge/GHCR-nuomiiiii%2Fkomari-2496ED?logo=docker)](https://github.com/nuomiiiii/komari/pkgs/container/komari)
[![License](https://img.shields.io/github/license/nuomiiiii/komari)](LICENSE)
[![Telegram](https://img.shields.io/badge/Telegram-Komari_Lite-26A5E4?logo=telegram&logoColor=white)](https://t.me/komari_lite)

Komari Lite 是一款轻量、自托管的服务器监控与运维管理工具。服务端提供 Web 管理界面，Agent 负责采集节点状态、执行延迟与回程线路探测，并在管理员授权后提供远程终端、文件管理和任务执行能力。

本项目基于 [komari-monitor/komari](https://github.com/komari-monitor/komari) 持续开发，重点改善低配置主控上的数据库占用、历史查询和维护负载，同时补充更完整的流量管理、备份迁移、接入安全与双端后台体验。

**当前正式版：[`2.1.12`](https://github.com/nuomiiiii/komari/releases/tag/2.1.12)**

[快速开始](#快速开始) · [功能概览](#功能概览) · [升级说明](#从旧版本升级) · [Agent](#agent-与远程管理) · [完整更新日志](https://github.com/nuomiiiii/komari/releases)

> [!WARNING]
> Komari Lite 只能部署在你拥有或已获得授权管理的设备上。请勿将其用于未经授权的访问、持久化、命令执行或其他滥用行为。管理员应启用 HTTPS 与双因素认证，并妥善保护 Agent Token 和备份文件。

## 2.1.12 更新摘要

- **电脑端与手机端分别优化**：电脑端使用信息密度更高的列表，手机端使用自适应卡片；统一节点、延迟监测、回程线路、远程执行、通知与告警页面的字号、间距、按钮和交互。
- **长列表更易管理**：主要管理列表统一分页，每页显示 10 条；节点排序支持跨页拖动，并继续作为首页报表等页面的全局节点顺序。
- **仪表盘信息更完整**：集中展示节点在线率、离线节点、实时与计费流量、近一个月流量、数据库占用和当前回程线路监测状态。
- **数据库更省空间**：指标、延迟和丢包记录使用更紧凑的无损存储与分层接力。同一份已填满 7 天的实测数据库由 21.84 MiB 降至 18.81 MiB，减少约 **13.9%**；实际效果会随节点和任务规模变化。
- **低性能主控更稳定**：根据可用内存、CPU 核心数和容器限制调整缓存与查询并发；压缩、数据接力、WAL 维护和空间回收分批执行，降低单核、ARM64 和慢速磁盘上的瞬时压力。
- **流量与账单管理增强**：支持流量额度、重置日、计费周期、价格与货币设置，可校准当前周期流量；仪表盘、告警和日/周/月报告使用统一统计口径。
- **接入方式重新整理**：支持内置 HTTPS、IPv4/IPv6 监听、HTTP 自动跳转 HTTPS、HSTS、反向代理和 Cloudflare Tunnel，并提供更清晰的状态与日志界面。
- **备份迁移更完整**：完整备份与恢复保留历史监测数据，另提供“仅导出配置”；升级过程显示阶段、进度和错误，失败时回滚当前操作并保留原数据。
- **远程管理体验改进**：优化多标签、多节点连续连接和残留会话清理，兼容 Cloudflare Tunnel、常见反向代理、两步验证和离线节点场景。

## 功能概览

| 模块 | 现有能力 |
| --- | --- |
| 节点监控 | CPU、内存、磁盘、网络、负载、连接数、运行时间、在线状态与实时流量 |
| 节点管理 | 自动发现、分组、备注、标签、国家或地区修正、分页排序、账单、价格、货币、流量额度和重置日 |
| 延迟监测 | IPv4/IPv6 目标探测、任务与服务器视图、历史曲线、丢包统计、任务排序和异常告警 |
| 回程线路 | 电信、移动、联通线路识别，支持切线与恢复判断、监测记录、通知和仪表盘状态 |
| 通知与报告 | 通用通知、离线通知、负载通知、延迟监测告警、流量告警，以及日/周/月流量报告 |
| 远程管理 | 多标签 Web 终端、文件管理、远程任务执行和 Docker 管理；仅在 Agent 支持并由管理员主动发起时可用 |
| 数据与存储 | SQLite 占用明细、运行诊断、历史分层、迁移进度、WAL 维护、手动空间回收、完整备份恢复和仅配置导出 |
| 接入与安全 | 管理员登录、双因素认证、单点登录、会话管理、内置 HTTPS、反向代理和 Cloudflare Tunnel |
| 外观与多端 | 可安装主题与托管主题配置；电脑端列表、手机端卡片，并适配简体中文、繁体中文、英文、日文和印尼文 |
| 部署与更新 | Linux 一键安装与受控回退、Docker、Windows/Linux 多架构二进制和在线更新校验 |

## 快速开始

### Linux 一键安装

适用于使用 systemd 的常见 Linux 发行版：

```bash
curl -fsSL https://raw.githubusercontent.com/nuomiiiii/komari/main/install-komari.sh -o install-komari.sh
chmod +x install-komari.sh
sudo ./install-komari.sh
```

安装完成后访问 `http://<服务器 IP>:25774`。正式环境可在后台启用内置 HTTPS，也可以使用反向代理或 Cloudflare Tunnel 接入。

### Docker

```bash
mkdir -p ./data
docker run -d \
  --name komari \
  --restart unless-stopped \
  -p 25774:25774 \
  -v "$(pwd)/data:/app/data" \
  ghcr.io/nuomiiiii/komari:latest
```

固定使用当前正式版时，将镜像标签改为 `ghcr.io/nuomiiiii/komari:2.1.12`。

更新 Docker 部署前请先备份 `data` 目录，然后拉取新镜像并使用原来的端口和数据挂载重新创建容器：

```bash
docker pull ghcr.io/nuomiiiii/komari:latest
docker rm -f komari
docker run -d \
  --name komari \
  --restart unless-stopped \
  -p 25774:25774 \
  -v "$(pwd)/data:/app/data" \
  ghcr.io/nuomiiiii/komari:latest
```

不要在未确认备份的情况下删除宿主机数据目录。

### 二进制文件

从 [Releases](https://github.com/nuomiiiii/komari/releases) 下载与系统架构匹配的文件。正式版提供 Linux `386`、`amd64`、`arm64`、`loong64`、`riscv64`，以及 Windows `386`、`amd64`、`arm64` 构建。

```bash
chmod +x komari-linux-amd64
./komari-linux-amd64 server -l 0.0.0.0:25774
```

默认数据目录为程序工作目录下的 `data`。

## Agent 与远程管理

Agent 项目与安装说明见 [nuomiiiii/komari-agent](https://github.com/nuomiiiii/komari-agent)。`2.1.x` 服务端建议搭配当前兼容版本 Agent 使用；远程终端、文件管理、Docker 管理和任务执行只有在 Agent 支持并由管理员主动发起时才可用。

远程入口受管理员登录、双因素认证和短时会话限制。所选节点会保留在终端页面地址中，刷新、两步验证、连续打开和多标签页场景下不会串到其他服务器；这项能力不会修改系统 SSH、防火墙或其他远程连接配置。

## 数据库与升级说明

### SQLite 指标存储

SQLite 指标库使用紧凑的无损编码和分层保留。压缩不会额外改变已采集指标的数值精度，详细指标和历史汇总继续遵循后台设置的保留周期；流量报告所需的累计值由独立精确账本保存。

数据库缓存、查询并发和后台维护批次会根据主控资源自动调整。已经形成的历史数据堆积会从保存位置继续处理，每批成功后才清理对应旧数据；失败时保留原数据并在后续重试。

这套迁移只适用于 SQLite 指标存储。外置 MySQL 或 PostgreSQL 不会进入 SQLite 指标库迁移，也不会显示对应进度。

### 从旧版本升级

> [!IMPORTANT]
> `2.1.12` 包含指标数据库迁移。升级前请备份完整 `data` 目录；首次启动时不要中断迁移，并等待后台迁移页面明确显示完成。

1. 停止旧 Komari Lite，备份程序和完整 `data` 目录。
2. 更新程序或容器，并保持原有 `data` 挂载不变。
3. 打开管理页面查看迁移进度，确认服务正常后再删除备份。
4. SQLite 产生的空闲页会继续复用；如需立即把空间归还给磁盘，可在业务低峰期手动执行一次“回收空间”。

上游 `1.3.1`、`1.3.2` 以及本分支 `2.1.7` 至 `2.1.11` 的 SQLite 指标库会通过同一迁移页面转换为当前格式，原有数据保留周期不会被改回默认值。监控数据固定启用分层降采样，升级时会自动停用旧版低资源模式。

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

- [版本发布与完整更新日志](https://github.com/nuomiiiii/komari/releases)
- [Komari Lite 文档](https://nuomiiiii.github.io/komari-document/)
- [Telegram 群组](https://t.me/komari_lite)
- [Komari Agent](https://github.com/nuomiiiii/komari-agent)
- [Komari Web](https://github.com/nuomiiiii/komari-web)
- [Nezha 主题](https://github.com/nuomiiiii/nezha)
- [上游 Komari](https://github.com/komari-monitor/komari)

## 致谢

感谢 Komari 上游维护者、Agent 与主题贡献者，以及持续提供测试和反馈的社区用户。本分支保留上游版权与 MIT 许可证。

## 许可证

[MIT License](LICENSE)
