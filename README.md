# 探针 Tanzhen

简洁、自托管的多 VPS 监控：Hub + Agent + 公开状态页。

**Tanzhen** is a minimal self-hosted multi-VPS monitor (Hub + Agent + status page). 中文文档在上，English summary at the end.

## 功能

- **一条指令部署主控**：`curl | sh` 拉起 Hub（二进制 + systemd），自带随机强密码与全部平台 agent 二进制
- **一条指令接入探针**：管理后台创建节点即得可复制命令，目标 VPS 执行即上线（Linux / macOS / Windows）
- **Hub**：创建节点、下发 Token；SQLite 持久化；公开状态页 + 独立管理后台
- **Agent**：采集 CPU / 内存 / Swap / 磁盘 / 上下行网速与累计流量；探测三网延迟与丢包；HTTP JSON 心跳（默认 2s）
- **状态页**：节点卡片 + 折线图（2 分钟滚动窗口）+ 利用率进度条 + 表格视图，深浅色主题，移动端友好
- **管理后台**（`/admin`）：用户名 + 密码登录（HTTP-only Session）；节点增删改；创建后立即展示可复制的 Linux / Windows 一键命令
- **节点元数据（Hub 侧可编辑）**：剩余流量配额、带宽、续费日期、价格、位置、备注
- **一键卸载**：Hub `--uninstall` / `--purge`（连数据目录）；Agent `--uninstall`（token 作为凭证始终清除）
- **明确不做**：远程命令执行、Web 终端、自动更新、插件市场 —— 探针只上报，不接受任何远端指令

## 30 秒部署主控

在要做主控的 VPS 上执行一条命令：

```sh
curl -fsSL https://cdn.jsdelivr.net/gh/FengBujue0104/tanzhen@main/cmd/hub/static/install-hub.sh | sh
```

脚本会自动：检测架构（amd64 / arm64…）→ 下载 Hub 与全部平台 agent 二进制 → 生成管理密码（预置 `ADMIN_PASSWORD='...'` 则用之，否则随机生成并打印一次）→ 推断 `PUBLIC_URL` → 注册 systemd 服务并开机自启 → 探活后打印状态页 / 管理后台地址。无需 Docker、无需 Go、无需克隆仓库。

随后：打开管理后台 → 新建节点 → 复制一键安装命令 → 到任意 VPS 粘贴执行，节点即上线。

```sh
# 自定义
curl -fsSL .../install-hub.sh | ADMIN_PASSWORD='换成一个强密码' TANZHEN_PORT=8080 PUBLIC_URL=http://1.2.3.4:8080 sh
# 卸载（保留数据 / 连数据）
curl -fsSL .../install-hub.sh | sh -s -- --uninstall [--purge]
```

> 已有一个 Hub 时，也可以从它安装第二个：`curl -fsSL 'http://HUB/install-hub.sh?hub=http://HUB' | sh`（二进制优先从原 Hub 镜像，适合内网 / 无法直连 GitHub 的环境）。

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

# 启动 Hub（ADMIN_PASSWORD 必填，见下）
export ADMIN_USER=admin
export ADMIN_PASSWORD=请换成强密码
export DATA_DIR=./data
export PORT=8080
export PUBLIC_URL=http://127.0.0.1:8080   # 生成一键安装链接时用
go run ./cmd/hub
```

打开 http://127.0.0.1:8080 查看公开状态页；管理请访问 http://127.0.0.1:8080/admin 。

### 创建节点

方式 A：浏览器打开 `/admin`，登录后填写节点信息并保存，界面立即给出可复制的安装命令。

方式 B：脚本调用管理 API。

```bash
curl -fsS -c /tmp/cj -X POST http://127.0.0.1:8080/api/admin/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"请换成强密码"}'

curl -fsS -b /tmp/cj -X POST http://127.0.0.1:8080/api/admin/nodes \
  -H 'Content-Type: application/json' \
  -d '{"name":"本地测试","meta":{"location":"本机","bandwidth":"1Gbps","traffic_quota":536870912000,"traffic_period":30}}'
```

`traffic_quota` 单位为**字节**：500 GiB = `536870912000`。设为 `0` 表示不限量，此时状态页改显示开机以来的累计流量。

### 启动本地 Agent

```bash
go run ./cmd/agent --hub http://127.0.0.1:8080 --token <上一步返回的 token>
```

## Docker Compose

```bash
export ADMIN_USER=admin            # 可选
export ADMIN_PASSWORD=请换成强密码  # 必填，未设置则容器拒绝启动
export PUBLIC_URL=http://你的服务器IP:8080
docker compose up -d --build
```

数据卷：`tanzhen-data` → 容器 `/data`。

`ADMIN_PASSWORD` 在 `docker-compose.yml` 里写作 `${ADMIN_PASSWORD:?...}`，省略时 compose 会直接报错 —— 这是有意的：用内置默认密码启动等于开放一个无鉴权的管理 API。

## 管理登录

| 变量 | 默认 | 说明 |
|------|------|------|
| `ADMIN_USER` | `admin` | 管理后台用户名 |
| `ADMIN_PASSWORD` | （必填） | 管理密码。留空或为内置的 `changeme` 时 Hub **拒绝启动** |
| `ALLOW_DEFAULT_PASSWORD` | `0` | 设为 `1` 才允许用 `changeme` 启动，仅供本地测试 |
| `ADMIN_TOKEN` | （无） | **迁移兼容**：未设 `ADMIN_PASSWORD` 时作为密码 |
| `ADMIN_API_TOKEN` | （无） | 脚本专用静态令牌，可放 `X-Admin-Token` 头 |

登录后使用 **HTTP-only Cookie Session**（默认约 7 天）。`/api/admin/*` 受 Session 保护；脚本亦可继续传 `X-Admin-Token: <ADMIN_API_TOKEN>` 或 `Authorization: Bearer <ADMIN_API_TOKEN>`。

退出：管理页「退出」或 `POST /api/admin/logout`。

## 管理后台单独隔离

`ADMIN_ADDR` 把管理后台绑到一个独立的地址/端口。它与公开监听是两棵独立的 handler 树，公开那棵上根本没有 `/api/admin/*`：

```bash
ADMIN_ADDR=127.0.0.1:8443 ./dist/tanzhen-hub
# 公开状态页 :8080（任何人）
# /admin 与 /api/admin/* :8443（只监听回环）
```

再用防火墙或 SSH 隧道收紧 `8443`：

```bash
ssh -L 8443:127.0.0.1:8443 you@vps    # 本地打开 http://127.0.0.1:8443/admin
```

**不设 `ADMIN_ADDR` 时**，公开监听必须同时承载管理 API，否则无从登录；此时请务必设置强 `ADMIN_PASSWORD`。若已设 `ADMIN_ADDR` 但仍想在公开端口上开放管理 API（例如放在反代后面统一鉴权），设 `SHARE_ADMIN_API=1`。

## Linux / macOS 一键扎针

在 `/admin` 创建节点后，界面会**立即**展示可复制命令（Komari 风格）：

```bash
# 推荐：查询参数注入（复制即用）
curl -fsSL 'http://YOUR_HUB:8080/install.sh?hub=http://YOUR_HUB:8080&token=YOUR_NODE_TOKEN' | sh

# 等价：显式参数
curl -fsSL http://YOUR_HUB:8080/install.sh | sh -s -- --hub http://YOUR_HUB:8080 --token YOUR_NODE_TOKEN

# 卸载（移除服务、二进制与 token；/etc/tanzhen 与 Hub 共享，只会清除探针自身文件）
curl -fsSL http://YOUR_HUB:8080/install.sh | sh -s -- --uninstall
```

脚本会：

1. 检测系统与架构：`linux` / `darwin`（macOS 走 nohup 兜底，无 launchd 服务）；`amd64` / `arm64` / `arm` / `386` / `riscv64` / `loong64`
2. 从 Hub `/releases/`（或 GitHub Releases）下载对应 agent
3. 把 token 写入 `/etc/tanzhen/token`（`0600`），**不走命令行参数** —— 避免泄露进 `/proc/cmdline` 与 `ps` 输出
4. 注册服务，按检测顺序：**systemd** → **procd**（OpenWrt） → **OpenRC**（Alpine） → 都不具备时用 `nohup` 兜底
5. 启动并打印状态

脚本是 POSIX sh，Alpine 的 busybox ash 也能直接跑：没有 `[[ ]]`、没有 `pipefail`。

用一键脚本部署的 Hub 已自带全部平台 agent 二进制（`/var/lib/tanzhen/releases/`），无需额外准备；本地自建 Hub 请先编译再以 `RELEASES_DIR=./releases` 启动：

```bash
./scripts/build.sh
```

## Windows Agent

管理员 PowerShell：

```powershell
# 推荐：带 token 的一键
irm 'http://YOUR_HUB:8080/install.ps1?hub=http://YOUR_HUB:8080&token=YOUR_NODE_TOKEN' | iex

# 或分两步
$env:TANZHEN_HUB='http://YOUR_HUB:8080'; $env:TANZHEN_TOKEN='YOUR_NODE_TOKEN'
irm 'http://YOUR_HUB:8080/install.ps1' | iex
```

脚本会固定 TLS 1.2+（PowerShell 5.1 默认协商 TLS 1.0，多数 CDN 已拒绝）、把 token 写入 ACL 收紧的文件、以 `--token-file` 启动 agent，并注册开机自启的计划任务 `TanzhenAgent`（失败重启 999 次）。

或手动：

```powershell
.\tanzhen-agent-windows-amd64.exe --hub http://YOUR_HUB:8080 --token YOUR_NODE_TOKEN
```

## 环境变量（Hub）

| 变量 | 默认 | 说明 |
|------|------|------|
| `ADDR` / `PORT` | `:8080` / `8080` | 公开监听地址；`ADDR` 优先 |
| `ADMIN_ADDR` | （无） | 管理后台独立监听地址，见上 |
| `DATA_DIR` | `./data` | SQLite 目录 |
| `PUBLIC_URL` | 空 | 对外 Hub 地址（一键安装链接）；留空则取请求的 Host |
| `RELEASES_DIR` | （无） | agent 二进制下载目录；未设则从 GitHub Releases 拉 |
| `SESSION_TTL` | `168h` | 登录会话有效期 |
| `LOGIN_LIMIT` / `LOGIN_WINDOW` | `5` / `5m` | 登录限流 |
| `TRUST_PROXY` | `0` | 是否信任 `X-Forwarded-For` / `X-Real-IP` |
| `COOKIE_SECURE` | 自动 | 强制 Session Cookie 带 `Secure`（HTTPS 反代后需为 `1`） |
| `OFFLINE_AFTER` | `30s` | 离线判定阈值 |
| `HISTORY_POINTS` | `60` | 折线图内存环形缓冲长度（60 × 2s = 2 分钟） |
| `SHARE_ADMIN_API` | 见上 | 已设 `ADMIN_ADDR` 时，是否仍公开管理 API |

## API 摘要

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/status` | 公开节点状态（卡片与表格视图的数据源） |
| POST | `/api/agent/heartbeat` | Agent 上报（头 `X-Agent-Token`） |
| GET | `/healthz` | 存活探针 |
| POST | `/api/admin/login` | 用户名密码登录（设 Session Cookie） |
| POST | `/api/admin/logout` | 退出登录 |
| GET | `/api/admin/me` | 当前登录状态 |
| GET/POST | `/api/admin/nodes` | 节点列表 / 创建 |
| PATCH/DELETE | `/api/admin/nodes/{id}` | 改 / 删节点 |
| GET | `/api/admin/nodes/{id}/install` | 安装命令 / URL |

Agent token 只从 `X-Agent-Token` 头读取，绝不接受查询参数 —— 否则每次上报都会把 token 写进代理访问日志和 shell 历史。

## 三网探测

Agent 对电信 / 联通 / 移动目标做 **TCP 探测**（优先 `:80`，失败则 `:443`），统计延迟与丢包。默认目标：

| 运营商 | 主要 IP | 备注 |
|--------|---------|------|
| 电信 CT | `183.60.83.19`, `202.96.128.86` | DNSPod / 广州电信 DNS |
| 联通 CU | `202.106.0.20`, `221.5.88.88` | 北京联通 / 广东联通 DNS |
| 移动 CM | `211.136.192.6`, `221.130.33.52` | 移动 DNS |

默认每 **30s** 探测一轮（每目标 4 次），心跳仍按 2s 上报（携带最近一次探测结果）。未使用原始 ICMP，以避免容器/权限限制。

## 交叉编译

```bash
./scripts/build.sh
# 产出:
#   dist/tanzhen-hub-linux-amd64|arm64
#   releases/tanzhen-hub-linux-amd64|arm64
#   releases/tanzhen-agent-linux-amd64|arm64|arm|386|riscv64|loong64
#   releases/tanzhen-agent-darwin-amd64|arm64
#   releases/tanzhen-agent-windows-amd64.exe|arm64.exe
```

## 目录结构

```
cmd/hub          Hub 入口 + 内嵌静态页（static/ 即全部前端与安装脚本）
cmd/agent        Agent 入口
internal/hub     HTTP API + Session 认证 + SQLite + 限流
internal/agent   采集 / 三网探测 / 上报
internal/models  共享结构体
scripts/build.sh 交叉编译
docker-compose.yml
```

## 安全说明

- Hub 拒绝以内置 `changeme` 密码启动；`ADMIN_PASSWORD` 未设置时 Docker Compose 直接失败
- Session Cookie 为 `HttpOnly` + `SameSite=Lax`，HTTPS 反代后自动带 `Secure`
- 所有响应带 CSP `default-src 'none'; script-src 'self'; style-src 'self'`（无 `unsafe-inline`，故 HTML 中没有任何内联 `style`）、`X-Content-Type-Options: nosniff`、`frame-ancestors 'none'`、`Referrer-Policy: no-referrer`
- Agent token 只经 `--token-file` 传递，不进命令行参数；落盘文件 `0600`（Windows 收紧 ACL）
- Agent **只上报**，没有任何接收远端指令的入口

## 开发与测试

```bash
go test ./...
go vet ./...
./scripts/build.sh
```

`cmd/hub/static_test.go` 会校验内嵌前端不含内联 `style` 属性 / `<style>` 块 / `innerHTML` —— CSP 禁掉这三者，一旦有人手滑加回去，测试立刻失败。

---

## English (brief)

Self-hosted Hub + Agent monitor for multiple VPS nodes. Public status page at `/` with sparklines, utilization meters and a table view in both themes; admin UI at `/admin` behind username/password session login (`ADMIN_USER` / `ADMIN_PASSWORD`; the hub refuses to boot on the built-in `changeme` unless `ALLOW_DEFAULT_PASSWORD=1`). Deploy a hub with one command — `curl -fsSL https://cdn.jsdelivr.net/gh/FengBujue0104/tanzhen@main/cmd/hub/static/install-hub.sh | sh` — which installs the binary as a systemd service and pre-stages every agent binary; then add nodes from `/admin` with a copy-paste one-liner (`curl '.../install.sh?hub=...&token=...' | sh` on Linux/macOS, `irm '.../install.ps1?...' | iex` on Windows). Both installers support `--uninstall`. Set `ADMIN_ADDR` to bind the admin UI and API to a separate listener. Agents report CPU / memory / swap / disks / network rates / cumulative traffic plus TCP latency and packet loss to the three Chinese carriers. **No remote command execution, no web terminal, no auto-update** — the agent only ever sends data.

```bash
ADMIN_PASSWORD=strong-secret go run ./cmd/hub
# open /admin to create nodes and copy install commands
go run ./cmd/agent --hub http://127.0.0.1:8080 --token <token>
# or: ADMIN_PASSWORD=strong-secret docker compose up -d --build
```
