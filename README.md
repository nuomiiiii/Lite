# Lite

![Lite](docs/assets/branding/komari-banner.svg)

[![Release](https://img.shields.io/github/v/release/nuomiiiii/lite?label=release)](https://github.com/nuomiiiii/lite/releases)
[![Docker](https://img.shields.io/badge/GHCR-nuomiiiii%2Flite-2496ED?logo=docker)](https://github.com/nuomiiiii/lite/pkgs/container/lite)
[![Telegram](https://img.shields.io/badge/Telegram-Lite-26A5E4?logo=telegram&logoColor=white)](https://t.me/komari_lite)
[![License](https://img.shields.io/github/license/nuomiiiii/lite)](LICENSE)

Lite 是一款轻量、自托管的服务器监控与运维管理工具。服务端提供 Web 管理界面，Agent 负责采集节点状态、执行延迟与回程线路探测，并在管理员授权后提供远程终端、文件管理和任务执行能力。

本项目基于 [komari-monitor/komari](https://github.com/nuomiiiii/lite) 持续开发，重点改善低配置主控上的数据库占用、历史查询和维护负载，同时补充更完整的流量管理、备份迁移、接入安全与双端后台体验。

**当前正式版：[`2.3.0`](https://github.com/nuomiiiii/lite/releases/tag/2.3.0)**

> [!IMPORTANT]
> 从 `2.2.1` 开始，系统 Web UI 与公开大屏主题已经解耦：Lite Web 只负责管理后台、远程终端等系统页面，主题只影响公开大屏。默认和保底主题都是 [Lite-Theme](https://github.com/nuomiiiii/Lite-theme)，可独立更新，并在已有其他可用主题时删除；主题管理始终要求至少保留一个可用主题。原经典主题已拆分为独立的 [komari-Classic](https://github.com/nuomiiiii/lite-Classic)，不再随 Lite 内置。从 Nezha / 旧默认主题升级时会迁到 Lite-Theme。

[快速开始](#快速开始) · [功能概览](#功能概览) · [升级说明](#从旧版本升级) · [Agent](#agent-与远程管理) · [完整更新日志](https://github.com/nuomiiiii/lite/releases)

> [!WARNING]
> Lite 只能部署在你拥有或已获得授权管理的设备上。请勿将其用于未经授权的访问、持久化、命令执行或其他滥用行为。管理员应启用 HTTPS 与双因素认证，并妥善保护 Agent Token 和备份文件。

## 2.3.0 稳定版能力摘要

- **产品更名为 Lite**：新安装使用 Lite 程序、数据目录和在线更新清单；管理后台、安装脚本和 Docker 镜像与此对齐。从 Komari、Komari Lite 或 Lite 2.2.x 升级时，会沿用原来的监听端口、站点设置、账号、主题和节点数据。
- **账单中心**：集中查看服务器资费、到期时间、本月/本年费用、附加费用和剩余价值，支持按币种汇总、筛选和作废冲销。
- **管理后台换新**：登录、侧栏、服务器列表、设置与安装/升级引导使用同一套布局；服务器详情改为独立页面。
- **节点安装改为 Lite-agent**：一键安装使用官方脚本和镜像；已在使用的节点可继续上报。
- **默认公开主题为 Lite-Theme**：保底主题同步更换，从 Nezha / 旧默认主题升级时会迁到 Lite-Theme。
- **回程线路规则自动更新**：默认从 Lite 仓库拉取 BGP 前缀库。

## 功能概览

| 模块 | 现有能力 |
| --- | --- |
| 节点监控 | CPU、内存、磁盘、网络、负载、连接数、运行时间、在线状态与实时流量 |
| 节点管理 | 自动发现、分组、备注、标签、国家\地区修正、分页排序、账单、价格、货币、流量额度和重置日，以及 Agent 配置上报、在线下发与结果确认 |
| 仪表盘配置 | 预制布局、模块开关、拖动排序、1/3、1/2 或整行宽度、实时与历史独立刷新、会话恢复，以及 Top 5/10/15/20 排行 |
| 延迟监测 | IPv4/IPv6 目标探测、历史曲线、平均时延、延迟抖动、近 15 分钟丢包排行、任务排序和异常告警 |
| 回程线路 | 电信、移动、联通线路识别，支持切线与恢复判断、监测记录、通知和仪表盘状态 |
| 通知与报告 | 通用通知、离线通知、负载当前告警与单机静默、延迟丢包告警、流量告警，以及日/周/月流量报告；离线、丢包和流量报告支持新服务器默认配置 |
| 远程管理 | 多标签 Web 终端、文件管理、远程任务执行和 Docker 管理；仅在 Agent 支持并由管理员主动发起时可用 |
| 数据与存储 | SQLite 占用明细、运行诊断、历史分层、上游 1.3.1/1.4.x 数据迁移、WAL 维护、手动空间回收、完整备份恢复和仅配置导出 |
| 接入与安全 | 管理员登录、双因素认证、单点登录、会话管理、API 与 WebSocket Origin 校验、内置 HTTPS、反向代理和 Cloudflare Tunnel |
| 外观与多端 | 系统 UI 与公开主题独立、主题市场、预览缓存、多语言主题信息、节点与 Ping 任务选择器、最后一个主题保护；电脑端列表、手机端卡片，并适配简体中文、繁体中文、英文、日文和印尼文 |
| 部署与更新 | Linux 一键安装与受控回退、Docker、Windows/Linux 多架构二进制和在线更新校验 |

## 快速开始

### Linux 一键安装

适用于使用 systemd 的常见 Linux 发行版：

```bash
curl -fsSL https://raw.githubusercontent.com/nuomiiiii/lite/main/install-lite.sh -o install-lite.sh
chmod +x install-lite.sh
sudo ./install-lite.sh
```

安装完成后访问 `http://<服务器 IP>:27777`。正式环境可在后台启用内置 HTTPS，也可以使用反向代理或 Cloudflare Tunnel 接入。

### Docker

```bash
mkdir -p ./data
docker run -d \
  --name lite \
  --restart unless-stopped \
  -p 27777:27777 \
  -v "$(pwd)/data:/app/data" \
  ghcr.io/nuomiiiii/lite:latest
```

固定使用当前正式版时，将镜像标签改为 `ghcr.io/nuomiiiii/lite:2.3.0`。

更新 Docker 部署前请先备份 `data` 目录，然后拉取新镜像并使用原来的端口和数据挂载重新创建容器：

```bash
docker pull ghcr.io/nuomiiiii/lite:latest
docker rm -f lite
docker run -d \
  --name lite \
  --restart unless-stopped \
  -p 27777:27777 \
  -v "$(pwd)/data:/app/data" \
  ghcr.io/nuomiiiii/lite:latest
```

不要在未确认备份的情况下删除宿主机数据目录。

### 二进制文件

从 [Releases](https://github.com/nuomiiiii/lite/releases) 下载与系统架构匹配的文件。正式版提供 Linux `386`、`amd64`、`arm64`、`loong64`、`riscv64`，以及 Windows `386`、`amd64`、`arm64` 构建。

```bash
chmod +x Lite-linux-amd64
./Lite-linux-amd64 server -l 0.0.0.0:27777
```

默认数据目录为程序工作目录下的 `data`。

## Agent 与远程管理

Agent 项目与安装说明见 [nuomiiiii/Lite-agent](https://github.com/nuomiiiii/Lite-agent)。建议使用 [Agent Releases](https://github.com/nuomiiiii/Lite-agent/releases/latest) 中的最新稳定版，以获得完整的配置上报、在线下发和结果确认能力；远程终端、文件管理、Docker 管理和任务执行只有在 Agent 支持并由管理员主动发起时才可用。

远程入口受管理员登录、双因素认证和短时会话限制。所选节点会保留在终端页面地址中，刷新、两步验证、连续打开和多标签页场景下不会串到其他服务器；这项能力不会修改系统 SSH、防火墙或其他远程连接配置。

## 数据库与升级说明

### SQLite 指标存储

SQLite 指标库使用紧凑的无损编码和分层保留。压缩不会额外改变已采集指标的数值精度，详细指标和历史汇总继续遵循后台设置的保留周期；流量报告所需的累计值由独立精确账本保存。

数据库缓存、查询并发和后台维护批次会根据主控资源自动调整。已经形成的历史数据堆积会从保存位置继续处理，每批成功后才清理对应旧数据；失败时保留原数据并在后续重试。

这套迁移只适用于 SQLite 指标存储。外置 MySQL 或 PostgreSQL 不会进入 SQLite 指标库迁移，也不会显示对应进度。

### 从旧版本升级

> [!IMPORTANT]
> `2.1.12` 包含指标数据库迁移。升级前请备份完整 `data` 目录；首次启动时不要中断迁移，并等待后台迁移页面明确显示完成。

1. 停止旧 Lite，备份程序和完整 `data` 目录。
2. 更新程序或容器，并保持原有 `data` 挂载不变。
3. 打开管理页面查看迁移进度，确认服务正常后再删除备份。
4. SQLite 产生的空闲页会继续复用；如需立即把空间归还给磁盘，可在业务低峰期手动执行一次“回收空间”。

上游 `1.3.1`、`1.3.2`、`1.4.x` 以及本分支 `2.1.7` 至 `2.1.11` 的 SQLite 指标库会通过同一迁移页面转换为当前格式，原有数据保留周期不会被改回默认值。监控数据固定启用分层降采样，升级时会自动停用旧版低资源模式。

## Linux 一键更新与回退

一键更新只面向官方脚本安装、由 systemd 管理且满足运行时检查的 Linux 实例。更新程序会校验版本、构建标识、大小和 SHA-256，备份当前程序及 `data`，并在新进程健康检查失败时恢复旧版本。

Docker、Windows、非 systemd 环境、外置指标数据库和不满足原子回退条件的部署不会启用该入口。详细限制见 [Linux 一键更新与回退](docs/self-update.md)。

## 从源码构建

后端使用 Go `1.25`，前端建议使用 Node.js `20+`：

```bash
git clone https://github.com/nuomiiiii/Lite.git
cd Lite
go build -o Lite
./Lite server -l 0.0.0.0:27777
```

正式构建会将 [Lite Web](https://github.com/nuomiiiii/Lite-web) 作为独立系统 UI 构建，并把产物放入 `web/public/systemUI/dist`；公开大屏主题则保存在独立的主题目录中。正式版本必须使用与 Lite 相同版本的 Lite Web 标签，可参考仓库内的 GitHub Actions 构建流程。

## 相关链接

- [版本发布与完整更新日志](https://github.com/nuomiiiii/lite/releases)
- [Lite 文档](https://nuomiiiii.github.io/komari-document/)
- [Telegram 群组](https://t.me/komari_lite)
- [Lite Agent](https://github.com/nuomiiiii/Lite-agent)
- [Lite Web](https://github.com/nuomiiiii/Lite-web)
- [Lite-Theme](https://github.com/nuomiiiii/Lite-theme)
- [上游 Komari](https://github.com/komari-monitor/komari)

## 致谢

感谢上游维护者、Agent 与主题贡献者，以及持续提供测试和反馈的社区用户。本分支保留上游版权与 MIT 许可证。

## 许可证

[MIT License](LICENSE)
