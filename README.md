# 探针 Tanzhen

简洁、美观的自托管多 VPS 监控：Hub + Agent + 多节点状态页。

**Tanzhen** is a minimal self-hosted multi-VPS monitor (Hub + Agent + status page). Chinese docs below; English summary at the end.

## 功能

- **Hub**：创建节点、下发 Token、Komari 风格一键安装命令；SQLite 持久化；公开状态页 + 独立管理后台
- **Agent**：采集 CPU / 内存 / Swap / 磁盘 / 上下行网速；探测三网延迟与丢包；HTTP JSON 心跳（默认 2s）
- **状态页**：深色卡片网格、在线/离线徽标、中文标签、移动端友好（公开，无管理弹窗）
- **管理后台**（`/admin`）：用户名 + 密码登录（HTTP-only Session）；节点增删改；创建后立即展示可复制的 Linux / Windows 一键命令
- **节点元数据（Hub 侧可编辑）**：剩余流量、带宽、续费日期、价格、位置等
- **不做**：主题市场、Web 终端、插件、复杂告警、远程命令执行

## 架构

```
浏览器 ──► Hub (:8080) ◄── Agent (各 VPS)
              │
           SQLite
```

- 公开状态页：`/`
- 管理后台：`/admin`（需登录）
- 离线判定：约 **30 秒**无心跳视为离线。

## 快速开始（本地，无 Docker）

依赖：Go 1.24+（go.mod 为 1.25；可用 Go toolchain 自动下载）。

```bash
cd tanzhen
go mod tidy

# 终端 1：启动 Hub
export ADMIN_USER=admin
export ADMIN_PASSWORD=changeme
export DATA_DIR=./data
export PORT=8080
export PUBLIC_URL=http://127.0.0.1:8080   # 生成一键安装链接时用
go run ./cmd/hub

# 终端 2：登录管理 API 创建节点并启动本地 Agent
# 方式 A：Session Cookie（浏览器访问 /admin）
# 方式 B：脚本仍可用 X-Admin-Token（值为管理员密码）
TOKEN=$(curl -fsS -X POST http://127.0.0.1:8080/api/admin/nodes \
  -H "X-Admin-Token: changeme" \
  -H "Content-Type: application/json" \
  -d '{"name":"本地测试","meta":{"location":"本机","bandwidth":"1Gbps","traffic_remain":"无限","price":"-","renewal_date":"-"}}' \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['token'])")

go run ./cmd/agent --hub http://127.0.0.1:8080 --token "$TOKEN"
```

打开 http://127.0.0.1:8080 查看公开状态页；管理请访问 http://127.0.0.1:8080/admin （默认 `admin` / `changeme`）。

## Docker Compose

```bash
export ADMIN_USER=admin
export ADMIN_PASSWORD=请换成强密码
export PUBLIC_URL=http://你的服务器IP:8080
docker compose up -d --build
```

数据卷：`tanzhen-data` → 容器 `/data`。

## 管理登录

| 变量 | 默认 | 说明 |
|------|------|------|
| `ADMIN_USER` | `admin` | 管理后台用户名 |
| `ADMIN_PASSWORD` | `changeme` | 管理密码（启动时 bcrypt 校验） |
| `ADMIN_TOKEN` | （无） | **迁移兼容**：若未设置 `ADMIN_PASSWORD`，则用 `ADMIN_TOKEN` 作为密码；二者都未设时密码为 `changeme` |

登录后使用 **HTTP-only Cookie Session**（约 7 天）。`/api/admin/*` 受 Session 保护；脚本亦可继续传 `X-Admin-Token: <密码>` 或 `Authorization: Bearer <密码>`。

退出：管理页「退出」或 `POST /api/admin/logout`。

## Linux 一键扎针

在 `/admin` 创建节点后，界面会**立即**展示可复制命令（Komari 风格）：

```bash
# 推荐：查询参数注入（复制即用）
curl -fsSL 'http://YOUR_HUB:8080/install.sh?hub=http://YOUR_HUB:8080&token=YOUR_NODE_TOKEN' | bash

# 等价：显式参数
curl -fsSL http://YOUR_HUB:8080/install.sh | bash -s -- --hub http://YOUR_HUB:8080 --token YOUR_NODE_TOKEN
```

脚本会：检测架构（amd64/arm64）→ 从 Hub `/releases/`（或 GitHub Releases）下载 agent → 写入 `/etc/tanzhen/agent.env` → 安装 **systemd** 或 **OpenRC（Alpine）** 服务。

请先把交叉编译的 agent 放到 Hub 的 `RELEASES_DIR`（默认 `./releases`）：

```bash
./scripts/build.sh
# 然后以 RELEASES_DIR=./releases 启动 hub，或挂载到 Docker
```

## Windows Agent

管理员 PowerShell：

```powershell
# 推荐：带 token 的一键（Hub 注入后自动 Install-Tanzhen）
irm 'http://YOUR_HUB:8080/install.ps1?hub=http://YOUR_HUB:8080&token=YOUR_NODE_TOKEN' | iex

# 或分两步
irm http://YOUR_HUB:8080/install.ps1 | iex
Install-Tanzhen -HubUrl 'http://YOUR_HUB:8080' -Token 'YOUR_NODE_TOKEN'
```

或手动：

```powershell
.\tanzhen-agent-windows-amd64.exe --hub http://YOUR_HUB:8080 --token YOUR_NODE_TOKEN
```

## 环境变量（Hub）

| 变量 | 默认 | 说明 |
|------|------|------|
| `PORT` | `8080` | 监听端口 |
| `DATA_DIR` | `./data` | SQLite 目录 |
| `ADMIN_USER` | `admin` | 管理用户名 |
| `ADMIN_PASSWORD` | `changeme` | 管理密码（优先） |
| `ADMIN_TOKEN` | — | 未设 `ADMIN_PASSWORD` 时作为密码（迁移） |
| `PUBLIC_URL` | 空 | 对外 Hub 地址（一键安装链接） |
| `RELEASES_DIR` | `./releases` | agent 二进制下载目录 |

## API 摘要

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/status` | 公开节点状态 |
| POST | `/api/agent/heartbeat` | Agent 上报（头 `X-Agent-Token`） |
| POST | `/api/admin/login` | 用户名密码登录（设 Session Cookie） |
| POST | `/api/admin/logout` | 退出登录 |
| GET | `/api/admin/me` | 当前登录状态 |
| POST | `/api/admin/nodes` | 创建节点（Session 或 `X-Admin-Token`） |
| GET/PATCH/DELETE | `/api/admin/nodes` / `{id}` | 管理节点 |
| GET | `/api/admin/nodes/{id}/install` | 安装命令 / URL |

## 三网探测

Agent 对电信 / 联通 / 移动目标做 **TCP 探测**（优先 `:80`，失败则 `:443`），统计延迟与丢包。默认目标：

| 运营商 | 主要 IP | 备注 |
|--------|---------|------|
| 电信 CT | `183.60.83.19`, `202.96.128.86` | DNSPod / 广州电信 DNS |
| 联通 CU | `202.106.0.20`, `221.5.88.88` | 北京联通 / 广东联通 DNS |
| 移动 CM | `211.136.192.6`, `221.130.33.52` | 移动 DNS |

默认每 **30s** 探测一轮（每目标 4 次），心跳仍按 1–3s 上报（携带最近一次探测结果）。未使用原始 ICMP，以避免容器/权限限制；若需 ICMP 可自行扩展。

## 交叉编译

```bash
./scripts/build.sh
# 产出:
#   dist/tanzhen-hub-linux-amd64|arm64
#   releases/tanzhen-agent-linux-amd64|arm64
#   releases/tanzhen-agent-windows-amd64.exe
```

## 目录结构

```
cmd/hub          Hub 入口（embed 静态页，含 admin.html）
cmd/agent        Agent 入口
internal/hub     HTTP API + Session 认证 + SQLite
internal/agent   采集 / 三网探测 / 上报
internal/models  共享结构体
web/static       前端源（与 cmd/hub/static 同步）
scripts/         install.sh / install.ps1 / build.sh
docker-compose.yml
```

## 开发与测试

```bash
go test ./...
go build -o dist/tanzhen-hub ./cmd/hub
go build -o dist/tanzhen-agent ./cmd/agent
```

---

## English (brief)

Self-hosted Hub + Agent monitor for multiple VPS nodes. Public status page at `/`; admin UI at `/admin` with username/password session login (`ADMIN_USER` / `ADMIN_PASSWORD`, default `admin`/`changeme`; legacy `ADMIN_TOKEN` used as password if `ADMIN_PASSWORD` unset). One-line Linux install via `curl '.../install.sh?hub=...&token=...' | bash` and Windows PowerShell helper. No plugins, web terminal, or remote exec.

```bash
ADMIN_USER=admin ADMIN_PASSWORD=changeme go run ./cmd/hub
# open /admin to create nodes and copy install commands
go run ./cmd/agent --hub http://127.0.0.1:8080 --token <token>
# or: docker compose up -d --build
```
