<div align="center">

<h1>
  <img src="docs/assets/branding/komari-banner.svg" alt="Komari Lite" width="920">
</h1>

[![Release](https://img.shields.io/github/v/release/nuomiiiii/komari?label=release)](https://github.com/nuomiiiii/komari/releases/latest)
[![Docs](https://img.shields.io/badge/docs-online-0F766E)](https://nuomiiiii.github.io/komari-document/)
[![Docker](https://img.shields.io/badge/GHCR-nuomiiiii%2Fkomari-2496ED?logo=docker)](https://github.com/nuomiiiii/komari/pkgs/container/komari)
[![Telegram](https://img.shields.io/badge/Telegram-Komari_Lite-26A5E4?logo=telegram&logoColor=white)](https://t.me/komari_lite)
[![License](https://img.shields.io/github/license/nuomiiiii/komari)](LICENSE)

</div>

Komari Lite 是自托管的服务器监控与运维面板，基于 [komari-monitor/komari](https://github.com/komari-monitor/komari) 继续开发。服务端提供管理后台和公开大屏；节点上的 Agent 采集状态、探测延迟与回程线路。管理员授权后，还可以远程终端、文件管理和任务执行。

主控按低配机器来做：SQLite 占用、历史查询和维护负载都会收着写，适合一台小 VPS 长期跑。

> 只能部署在你拥有或已获授权管理的设备上，不要用于未授权访问或远程控制。正式环境请启用 HTTPS 和双因素认证，并保管好 Agent Token 与备份。相关风险见 [Huntress 分析](https://www.huntress.com/blog/komari-c2-agent-abuse)。

## 功能

<table>
<tr>
<td width="50%" valign="top">

**节点监控**

CPU、内存、磁盘、网络、负载、连接数、运行时间、在线状态，以及实时流量和计费流量。

**节点管理**

自动发现、分组、备注、标签、国家或地区修正、分页排序；账单、价格、货币、流量额度和重置日。

**仪表盘**

预制布局、模块开关、拖动排序；1/3、1/2 或整行宽度；实时概览和历史图表分开刷新；会话恢复，避免刷新整页闪白；资源、流量、时延、抖动和丢包排行。

**延迟与回程**

IPv4 / IPv6 目标探测，历史曲线、平均时延、抖动和近 15 分钟丢包；任务排序与丢包告警。电信、移动、联通回程识别，支持切线、恢复判断、监测记录和通知。

</td>
<td width="50%" valign="top">

**通知与报告**

通用通知渠道；离线、负载、延迟丢包和流量告警；负载告警可按规则静默；新任务和新服务器可套默认告警，已有配置不会被覆盖；日 / 周 / 月流量报告。

**远程管理**

多标签 Web 终端、文件管理、远程执行和 Docker 管理。仅在 Agent 支持、且由管理员主动发起时可用，不会改系统 SSH 或防火墙。

**数据与存储**

SQLite 占用明细、运行诊断、历史分层、迁移进度、WAL 维护、手动回收空间；完整备份恢复和仅配置导出。也支持把指标写到远程 MariaDB / MySQL / PostgreSQL。

**接入、外观与部署**

管理员登录、2FA、SSO、会话管理、内置 HTTPS、反向代理和 Cloudflare Tunnel。系统后台与公开主题分离；可安装、更新主题，至少保留一个可用主题。电脑端列表、手机端卡片；简体中文、繁体中文、英文、日文、印尼文。Linux 一键安装与受控回退、Docker、Windows / Linux 多架构二进制和在线更新校验。

</td>
</tr>
</table>

后台和远程终端由 [Komari Lite Web](https://github.com/nuomiiiii/komari-web) 提供，主题只影响公开大屏。默认集成 [Nezha](https://github.com/nuomiiiii/nezha)，可独立更新。原经典主题已拆到 [komari-Classic](https://github.com/nuomiiiii/komari-Classic)，不再随服务端内置。

## 安装

默认端口 `25774`，数据在工作目录下的 `data`。装好后打开 `http://<服务器 IP>:25774`。

### Linux

适用于 systemd 发行版：

```bash
curl -fsSL https://raw.githubusercontent.com/nuomiiiii/komari/main/install-komari.sh -o install-komari.sh
chmod +x install-komari.sh
sudo ./install-komari.sh
```

之后可在后台开内置 HTTPS，或用反向代理 / Cloudflare Tunnel。官方脚本安装且由 `komari.service` 管理时，支持校验后在线更新；失败会回退程序和 `data`。限制见 [Linux 一键更新](docs/self-update.md)。

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

固定版本把标签换成 [Releases](https://github.com/nuomiiiii/komari/releases/latest) 里的 `x.y.z`。更新前先备份 `data`，再拉新镜像并用原来的端口和挂载重建容器，不要删宿主机数据目录。

### 二进制

从 [Releases](https://github.com/nuomiiiii/komari/releases/latest) 下载对应架构：Linux `386` / `amd64` / `arm64` / `loong64` / `riscv64`，Windows `386` / `amd64` / `arm64`。

```bash
chmod +x komari-linux-amd64
./komari-linux-amd64 server -l 0.0.0.0:25774
```

## Agent

节点程序见 [nuomiiiii/komari-agent](https://github.com/nuomiiiii/komari-agent)。请优先用后台「添加节点」生成的安装命令，会带上面板地址和 Token。

完整的配置上报、在线下发和结果确认，需要兼容当前服务端的 Agent 版本。远程能力同样只在 Agent 支持时可用。

## 升级

1. 停服务，备份程序和完整 `data`。
2. 更新二进制、安装脚本或 Docker 镜像，保持原 `data` 路径。
3. 打开后台确认服务正常，再删备份。

`2.1.12` 起 SQLite 指标库有一次迁移。从上游 `1.3.1` / `1.3.2`，或本分支 `2.1.7`–`2.1.11` 升级时，首次启动不要中断，等迁移页显示完成。空闲页会继续复用；要立刻把空间还给磁盘，可在低峰点一次「回收空间」。外置 MySQL / PostgreSQL 不会走这条 SQLite 迁移。

逐次改动见 [Releases](https://github.com/nuomiiiii/komari/releases)。

<details>
<summary>从源码构建</summary>

后端 Go `1.25`，前端 Node.js `20+`。正式包会把 [Komari Lite Web](https://github.com/nuomiiiii/komari-web) 编进 `web/public/systemUI/dist`，公开主题放在独立主题目录。服务端和 Web 要用同一版本标签，构建流程见仓库 Actions。

```bash
git clone https://github.com/nuomiiiii/komari.git
cd komari
go build -o komari
./komari server -l 0.0.0.0:25774
```

</details>

## 相关项目

[文档](https://nuomiiiii.github.io/komari-document/)　·　[Web](https://github.com/nuomiiiii/komari-web)　·　[Agent](https://github.com/nuomiiiii/komari-agent)　·　[Nezha 主题](https://github.com/nuomiiiii/nezha)　·　[Classic 主题](https://github.com/nuomiiiii/komari-Classic)　·　[上游 Komari](https://github.com/komari-monitor/komari)　·　[Telegram](https://t.me/komari_lite)

感谢上游维护者、Agent 与主题贡献者，以及帮忙测试的用户。本分支保留上游版权，许可证为 [MIT](LICENSE)。
