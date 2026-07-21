# Komari 本地开发与发布

## 本机环境

本工作区已准备以下便携工具，不修改系统级安装：

- Git 2.55
- GitHub CLI 2.96
- Go 1.26
- Node.js 24
- Zig 0.14.1（用于 CGO/SQLite 编译）

后端仓库位于 `komari`，前端仓库位于同级目录 `komari-web`。前端当前使用分支 `komari-2.1.1`。

## 常用命令

在 `komari` 目录中运行：

```powershell
# 启动后端（缺少前端产物时会先自动构建）
.\scripts\dev.cmd server

# 启动前端热更新开发服务器，API 默认代理到 127.0.0.1:25774
.\scripts\dev.cmd web

# 构建前端并生成 work/bin/komari.exe
.\scripts\dev.cmd build

# 运行完整 Go 测试
.\scripts\dev.cmd test
```

后端默认地址为 `http://127.0.0.1:25774`，前端开发服务器通常为 `http://127.0.0.1:5173`。首次启动生成的管理员账号和密码会打印在后端控制台。

## 稳定版本规则

在 GitHub Actions 中手动运行 `Publish Stable Release`，填写：

- `version`：仅版本号，例如 `2.1.1`
- `version_hash`：可留空；留空时自动生成七位字母数字标识
- `target_ref`：默认 `main`
- `web_ref`：本次发布使用的 `komari-web` 分支或标签

发布后各处含义如下：

- 页面和日志显示：`2.1.1 (a1b2c3d)`
- Git 标签：`2.1.1`
- GitHub Release 标题和标签：`2.1.1`
- Docker 标签：`ghcr.io/nuomiiiii/komari:2.1.1` 和 `latest`
- Docker 平台：`linux/amd64`、`linux/arm64`

Release Notes 会生成“主要更新内容 / Bug修复 / 其他 / What's Changed”。配置 OpenAI Secrets 时会生成更精炼的中文摘要；未配置时会按提交类型稳定分类，不会只剩 Full Changelog。
