# Komari Lite

![Komari Lite](docs/assets/branding/komari-banner.svg)

[![Release](https://img.shields.io/github/v/release/nuomiiiii/komari?label=release)](https://github.com/nuomiiiii/komari/releases)
[![Docker](https://img.shields.io/badge/GHCR-nuomiiiii%2Fkomari-2496ED?logo=docker)](https://github.com/nuomiiiii/komari/pkgs/container/komari)
[![Telegram](https://img.shields.io/badge/Telegram-Komari_Lite-26A5E4?logo=telegram&logoColor=white)](https://t.me/komari_lite)
[![License](https://img.shields.io/github/license/nuomiiiii/komari)](LICENSE)

Komari Lite 是一款轻量、自托管的服务器监控与运维管理工具。服务端提供 Web 管理界面，Agent 负责采集节点状态、执行延迟与回程线路探测，并在管理员授权后提供远程终端、文件管理和任务执行能力。

本项目基于 [komari-monitor/komari](https://github.com/komari-monitor/komari) 持续开发，重点改善低配置主控上的数据库占用、历史查询和维护负载，同时补充更完整的流量管理、备份迁移、接入安全与双端后台体验。

**当前正式版：[`2.2.3`](https://github.com/nuomiiiii/komari/releases/tag/2.2.3)**

> [!IMPORTANT]
> 从 `2.2.1` 开始，系统 Web UI 与公开大屏主题已经解耦：Komari Lite Web 只负责管理后台、远程终端等系统页面，主题只影响公开大屏。默认集成的 Nezha 主题可独立更新，并可在已有其他可用主题时删除；主题管理始终要求至少保留一个可用主题。原经典主题已拆分为独立的 [komari-Classic](https://github.com/nuomiiiii/komari-Classic)，不再随 Komari Lite 内置。

[快速开始](#快速开始) · [功能概览](#功能概览) · [升级说明](#从旧版本升级) · [Agent](#agent-与远程管理) · [完整更新日志](https://github.com/nuomiiiii/komari/releases)

> [!WARNING]
> Komari Lite 只能部署在你拥有或已获得授权管理的设备上。请勿将其用于未经授权的访问、持久化、命令执行或其他滥用行为。管理员应启用 HTTPS 与双因素认证，并妥善保护 Agent Token 和备份文件。

## 2.2.3 稳定版能力摘要

- **通知管理更完整**：负载通知新增“当前告警”视图，并支持对单台服务器静默 24 小时、3 天、7 天或永久静默；静默只暂停消息发送，不停止检测和图表展示。离线通知、延迟丢包告警和流量定时报告可以设置“新服务器默认配置”，不会覆盖已有服务器。
- **后台加载更快、更稳定**：顶栏和侧栏在页面加载时保持可用，节点、详情和延迟任务复用共享数据；仪表盘先展示摘要、再在卡片内加载图表，并按 CPU 核数限制查询并发。登录后的页面预热按空闲时段和网络状况执行，避免弱网下争抢带宽。
- **主题与 PWA 体验完善**：修复主题上传后的预览显示，已安装主题和主题市场预览增加缓存；主题清单支持多语言名称、作者和描述，以及节点、Ping 任务选择器。PWA 会实时使用后台设置的站点名称、描述和 favicon。
- **大文件上传更可靠**：备份恢复、首次安装恢复和主题上传统一采用分块传输，支持失败分块重试、主动取消和合并校验；上传进度会先稳定到达 100%，再进入服务端处理阶段。
- **数据库迁移覆盖上游 1.4.x**：可识别上游 `metric_label_sets` 布局，并继续兼容 1.3.1 的 `metric_labels`；迁移在 SQLite 事务中执行，失败自动回滚，同时检测到两套完整布局时会拒绝误迁移。
- **Linux 安装与更新更稳妥**：安装和升级失败时恢复原状态；在线更新兼容内置 HTTPS，并避免回滚失败后辅助任务反复重启或错误信息无限累积。
- **回程线路识别持续更新**：补充 `210.78.0.0/19` 的联通 9929 覆盖，完善 CUG VIP 路径判定；规则仍可在后台查看、刷新和更新。
- **管理界面一致性提升**：统一主题、上传、账号、终端和后台管理弹窗的描述与可访问性契约，完善上传完成时序、取消状态和后台空闲预热。

## 功能概览

| 模块 | 现有能力 |
| --- | --- |
| 节点监控 | CPU、内存、磁盘、网络、负载、连接数、运行时间、在线状态与实时流量 |
| 节点管理 | 自动发现、分组、备注、标签、国家或地区修正、分页排序、账单、价格、货币、流量额度和重置日，以及 Agent 配置上报、在线下发与结果确认 |
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

固定使用当前正式版时，将镜像标签改为 `ghcr.io/nuomiiiii/komari:2.2.3`。

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

Agent 项目与安装说明见 [nuomiiiii/komari-agent](https://github.com/nuomiiiii/komari-agent)。建议使用 [Agent Releases](https://github.com/nuomiiiii/komari-agent/releases/latest) 中的最新稳定版，以获得完整的配置上报、在线下发和结果确认能力；远程终端、文件管理、Docker 管理和任务执行只有在 Agent 支持并由管理员主动发起时才可用。

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

上游 `1.3.1`、`1.3.2`、`1.4.x` 以及本分支 `2.1.7` 至 `2.1.11` 的 SQLite 指标库会通过同一迁移页面转换为当前格式，原有数据保留周期不会被改回默认值。监控数据固定启用分层降采样，升级时会自动停用旧版低资源模式。

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

正式构建会将 [Komari Lite Web](https://github.com/nuomiiiii/komari-web) 作为独立系统 UI 构建，并把产物放入 `web/public/systemUI/dist`；公开大屏主题则保存在独立的主题目录中。正式版本必须使用与 Komari Lite 相同版本的 Komari Lite Web 标签，可参考仓库内的 GitHub Actions 构建流程。

## 相关链接

- [版本发布与完整更新日志](https://github.com/nuomiiiii/komari/releases)
- [Komari Lite 文档](https://nuomiiiii.github.io/komari-document/)
- [Telegram 群组](https://t.me/komari_lite)
- [Komari Lite Agent](https://github.com/nuomiiiii/komari-agent)
- [Komari Lite Web](https://github.com/nuomiiiii/komari-web)
- [Nezha 主题](https://github.com/nuomiiiii/nezha)
- [上游 Komari](https://github.com/komari-monitor/komari)

## 致谢

感谢 Komari 上游维护者、Agent 与主题贡献者，以及持续提供测试和反馈的社区用户。本分支保留上游版权与 MIT 许可证。

## 许可证

[MIT License](LICENSE)
