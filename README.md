
# Komari

Komari 是一款轻量级的自托管服务器监控工具，旨在提供简单、高效的服务器性能监控解决方案。它支持通过 Web 界面查看服务器状态，并通过轻量级 Agent 收集数据。

[文档](https://komari-monitor.github.io/komari-document/)

## 特性
- **轻量高效**：低资源占用，适合各种规模的服务器。
- **自托管**：完全掌控数据隐私，部署简单。
- **Web 界面**：直观的监控仪表盘，易于使用。

## 快速开始

### 依赖
- Docker（快速部署）
- 或者 Go 1.18+ 和 Node.js 20+（手工构建）

### Docker 部署
1. 创建数据目录：
   ```bash
   mkdir -p ./data
   ```
2. 运行 Docker 容器：
   ```bash
   docker run -d \
     -p 25774:25774 \
     -v $(pwd)/data:/app/data \
     --name komari \
     ghcr.io/komari-monitor/komari:latest
   ```
3. 查看默认账号和密码：
   ```bash
   docker logs komari
   ```
4. 在浏览器中访问 `http://<your_server_ip>:25774`。

> [!NOTE]
> 你也可以通过环境变量 `ADMIN_USERNAME` 和 `ADMIN_PASSWORD` 自定义初始用户名和密码。

### 二进制文件部署
1. 访问 Komari 的 [GitHub Release 页面](https://github.com/komari-monitor/komari/releases) 下载适用于你操作系统的最新二进制文件。
2. 运行 Komari：
   ```bash
   ./komari server -l 0.0.0.0:25774
   ```
3. 在浏览器中访问 `http://<your_server_ip>:25774`，默认监听 `25774` 端口。
4. 默认账号和密码可在启动日志中查看，或通过环境变量 `ADMIN_USERNAME` 和 `ADMIN_PASSWORD` 设置。

> [!NOTE]
> 确保二进制文件具有可执行权限（`chmod +x komari`）。数据将保存在运行目录下的 `data` 文件夹中。


### 手工构建
1. 构建前端静态文件：
   ```bash
   git clone https://github.com/komari-monitor/komari-web
   cd komari-web
   npm install
   npm run build
   ```
2. 构建后端：
   ```bash
   git clone https://github.com/komari-monitor/komari
   cd komari
   ```
   将步骤1中生成的静态文件复制到 `komari` 项目根目录下的 `/public/dist` 文件夹。
   ```bash 
   go build -o komari
   ```
4. 运行：
   ```bash
   ./komari server -l 0.0.0.0:25774
   ```
   默认监听 `25774` 端口，访问 `http://localhost:25774`。

## 前端开发指南
这个坑晚点再填吧 ヽ(￣ω￣(￣ω￣〃)ゝ

## 客户端 Agent 开发指南
这个坑晚点再填吧 (o゜▽゜)o☆

## 贡献
欢迎提交 Issue 或 Pull Request！

## 鸣谢
 - 感谢我自己能这么闲

## 引用
 - [gorm.io](https://gorm.io/)
 - [spf13/cobra](https://github.com/spf13/cobra)
 - [oschwald/maxminddb-golang](https://github.com/oschwald/maxminddb-golang)
 - [gorilla/websocket](https://github.com/gorilla/websocket)
 - [google/uuid](https://github.com/google/uuid)
 - [gin-gonic/gin](https://github.com/gin-gonic/gin)
 - [UserExistsError/conpty](https://github.com/UserExistsError/conpty)
 - [creack/pty](https://github.com/creack/pty)
 - [rhysd/go-github-selfupdate](https://github.com/rhysd/go-github-selfupdate)
 - [shirou/gopsutil](https://github.com/shirou/gopsutil)

## 许可证
[MIT License](LICENSE)

## 截图

![PixPin_2025-06-07_15-28-30](https://github.com/user-attachments/assets/edce5694-c6c8-4647-bc11-8b27105fd55c)
![PixPin_2025-06-07_15-28-49](https://github.com/user-attachments/assets/23b58032-211a-4b59-b444-c8267606a0bb)
![PixPin_2025-06-07_15-29-05](https://github.com/user-attachments/assets/f4325d4d-fa69-41c6-9251-19f530476e65)
![PixPin_2025-06-07_15-30-05](https://github.com/user-attachments/assets/69be38f6-6681-4afb-9a04-965d39b7fcc2)
![PixPin_2025-06-07_15-30-46](https://github.com/user-attachments/assets/01d84dd1-3c81-4424-a041-7dbc2ae5822e)


## 碎碎念

### 我为什么做Komari？

起因： [求推荐系统监控软件](https://www.nodeseek.com/post-133745-1)

> @古xx斯
> 哪吒？
> > 数据至少保存15天

> @sxxu
> 怕不是做梦的预算

> @Vxxn
> prometheus加grafana
> > 部署太麻烦

没有合适的，最近刚好时间稍微多了一些，就搓了 komari.

