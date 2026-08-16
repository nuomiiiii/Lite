<div align="center">

# Komari Lite

<img src="docs/assets/branding/komari-banner.svg" alt="Komari Lite" width="920">

[![Release](https://img.shields.io/github/v/release/nuomiiiii/komari?label=release)](https://github.com/nuomiiiii/komari/releases/latest)
[![Docker](https://img.shields.io/badge/GHCR-nuomiiiii%2Fkomari-2496ED?logo=docker)](https://github.com/nuomiiiii/komari/pkgs/container/komari)
[![Telegram](https://img.shields.io/badge/Telegram-Komari_Lite-26A5E4?logo=telegram&logoColor=white)](https://t.me/komari_lite)
[![License](https://img.shields.io/github/license/nuomiiiii/komari)](LICENSE)

轻量、自托管的服务器监控与运维面板。

**正式版 [`2.2.3`](https://github.com/nuomiiiii/komari/releases/tag/2.2.3)**　·　[文档](https://nuomiiiii.github.io/komari-document/)　·　[更新日志](https://github.com/nuomiiiii/komari/releases)

</div>

Komari Lite 基于 [komari-monitor/komari](https://github.com/komari-monitor/komari) 继续开发。服务端提供管理后台和公开大屏，Agent 采集节点状态、做延迟与回程探测；管理员授权后还可以远程终端、文件管理和任务执行。主控面向低配机器：SQLite 占用、历史查询和维护负载都按弱机来收。

> 只能部署在你拥有或已获授权管理的设备上。不要用于未授权访问或远程控制。正式环境请启用 HTTPS 和双因素认证，并保管好 Agent Token 与备份。相关风险见 [Huntress 分析](https://www.huntress.com/blog/komari-c2-agent-abuse)。

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

固定版本用 `ghcr.io/nuomiiiii/komari:2.2.3`。更新前先备份 `data`，再拉新镜像并用原来的端口和挂载重建容器，不要删宿主机数据目录。

### 二进制

从 [Releases](https://github.com/nuomiiiii/komari/releases/latest) 下载对应架构：Linux `386` / `amd64` / `arm64` / `loong64` / `riscv64`，Windows `386` / `amd64` / `arm64`。

```bash
chmod +x komari-linux-amd64
./komari-linux-amd64 server -l 0.0.0.0:25774
```

## 能做什么

| 监控 | 管理 | 运维 |
| --- | --- | --- |
| CPU、内存、磁盘、网络、负载、连接数 | 自动发现、分组、标签、备注、地区 | 多标签 Web 终端 |
| 在线状态、实时与计费流量 | 账单、流量额度与重置日 | 文件管理、远程执行 |
| 延迟、抖动、近 15 分钟丢包 | 可配置仪表盘与刷新周期 | Docker 管理 |
| 电信 / 移动 / 联通回程线路 | 主题只影响公开大屏 | 离线、负载、丢包、流量告警 |

后台和远程终端由 [Komari Lite Web](https://github.com/nuomiiiii/komari-web) 提供，和公开大屏主题分开。默认带 Nezha 主题，可独立更新；至少保留一个可用主题。原经典主题在 [komari-Classic](https://github.com/nuomiiiii/komari-Classic)，不再内置。

界面支持电脑列表和手机卡片，语言有简体中文、繁体中文、英文、日文、印尼文。接入可用管理员登录、2FA、SSO、内置 HTTPS、反向代理和 Cloudflare Tunnel。

## Agent

节点程序见 [nuomiiiii/komari-agent](https://github.com/nuomiiiii/komari-agent)。当前服务端建议搭配 Agent **`2.2.0.1`** 或更高兼容版本。远程终端、文件、Docker 和任务执行只在 Agent 支持、且由管理员主动发起时可用，不会改系统 SSH 或防火墙。

请优先用后台「添加节点」生成的安装命令，会带上面板地址和 Token。

## 升级

1. 停服务，备份程序和完整 `data`。
2. 更新二进制、安装脚本或 Docker 镜像，保持原 `data` 路径。
3. 打开后台确认服务正常，再删备份。

`2.1.12` 起 SQLite 指标库有一次迁移。从上游 `1.3.1` / `1.3.2`，或本分支 `2.1.7`–`2.1.11` 升级时，首次启动不要中断，等迁移页显示完成。空闲页会继续复用；要立刻把空间还给磁盘，可在低峰点一次「回收空间」。外置 MySQL / PostgreSQL 不会走这条 SQLite 迁移。

当前版本的改动看 [Releases](https://github.com/nuomiiiii/komari/releases/tag/2.2.3)，不要把 README 当更新日志。

<details>
<summary>从源码构建</summary>

后端 Go `1.25`，前端 Node.js `20+`。正式包会把 [Komari Lite Web](https://github.com/nuomiiiii/komari-web) 编进 `web/public/systemUI/dist`，公开主题放在独立主题目录。服务端和 Web 要用同一 `x.y.z` 标签，构建流程见仓库 Actions。

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
