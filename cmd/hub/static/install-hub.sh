#!/bin/sh
# 探针 Tanzhen · 主控一键部署
#
# 用法（任选其一，CDN 在国内更稳）:
#   curl -fsSL https://cdn.jsdelivr.net/gh/FengBujue0104/tanzhen@main/cmd/hub/static/install-hub.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/FengBujue0104/tanzhen/main/cmd/hub/static/install-hub.sh | sh
#   curl -fsSL 'http://HUB/install-hub.sh?hub=http://HUB' | sh
#
#   预置管理密码（强烈推荐，否则随机生成一次）:
#   curl -fsSL ... | ADMIN_PASSWORD='换成一个强密码' sh
#
#   卸载:
#   curl -fsSL ... | sh -s -- --uninstall          # 保留数据
#   curl -fsSL ... | sh -s -- --uninstall --purge  # 连数据一起删除
#
# 环境变量:
#   ADMIN_PASSWORD    管理后台密码（缺失时交互输入或随机生成）
#   TANZHEN_PORT      Hub 监听端口 (默认 8080)
#   PUBLIC_URL        状态页对外地址（默认按公网 IP 推断）
#   TANZHEN_VERSION   版本标签 (默认 latest)
#   TANZHEN_BASE_URL  二进制下载基址，默认 GitHub Releases；自建镜像时可指向自己的 Hub
#   TANZHEN_DATA_DIR  数据目录 (默认 /var/lib/tanzhen)
#   INSTALL_DIR       二进制目录 (默认 /usr/local/bin)；与 CONFIG_DIR 一起可作为非 root 前缀
#   CONFIG_DIR        配置目录 (默认 /etc/tanzhen)
#   LOG_FILE / PID_FILE  nohup 日志与 pid（非 systemd 时）
#
# POSIX sh on purpose, same as install.sh: Alpine and other minimal images
# have no bash. The whole deployment is one static binary plus one systemd
# unit, so it stays light enough for the smallest VPS.
set -eu

REPO="https://github.com/FengBujue0104/tanzhen"
VERSION="${TANZHEN_VERSION:-latest}"
BASE_URL="${TANZHEN_BASE_URL:-$REPO/releases/$VERSION/download}"
HUB_PORT="${TANZHEN_PORT:-8080}"
# Prefix-friendly overrides: set INSTALL_DIR/CONFIG_DIR/DATA_DIR under a home
# or workspace path for non-root smoke installs. Defaults keep production paths.
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
CONFIG_DIR="${CONFIG_DIR:-/etc/tanzhen}"
DATA_DIR="${TANZHEN_DATA_DIR:-/var/lib/tanzhen}"
RELEASES_DIR="${RELEASES_DIR:-$DATA_DIR/releases}"
ENV_FILE="$CONFIG_DIR/hub.env"
LOG_FILE="${LOG_FILE:-/var/log/tanzhen-hub.log}"
PID_FILE="${PID_FILE:-/var/run/tanzhen-hub.pid}"
SERVICE="tanzhen-hub"
UNIT_FILE="${UNIT_FILE:-/etc/systemd/system/$SERVICE.service}"
UNINSTALL=0
PURGE=0

log() { printf '%s\n' "$*"; }
die() { printf '错误: %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'EOT'
探针 Tanzhen · 主控一键部署

  curl -fsSL https://cdn.jsdelivr.net/gh/FengBujue0104/tanzhen@main/cmd/hub/static/install-hub.sh | sh

可选: ADMIN_PASSWORD='...' TANZHEN_PORT=8080 PUBLIC_URL=http://IP:8080
卸载: ... | sh -s -- --uninstall [--purge]
EOT
  exit 0
}

while [ $# -gt 0 ]; do
  case "$1" in
    --uninstall) UNINSTALL=1; shift ;;
    --purge) PURGE=1; shift ;;
    -h|--help) usage ;;
    *) die "未知参数: $1" ;;
  esac
done

# Root, transparent sudo, or non-root prefix install.
# When INSTALL_DIR (and siblings) live under a writable tree, skip privilege
# escalation entirely — used by CI / local smoke tests under a workspace prefix.
SUDO=""
NEED_PRIV=1
if [ "$(id -u)" -eq 0 ]; then
  NEED_PRIV=0
elif mkdir -p "$INSTALL_DIR" "$CONFIG_DIR" "$DATA_DIR" 2>/dev/null \
  && [ -w "$INSTALL_DIR" ] && [ -w "$CONFIG_DIR" ] && [ -w "$DATA_DIR" ]; then
  NEED_PRIV=0
fi
if [ "$NEED_PRIV" = 1 ]; then
  command -v sudo >/dev/null 2>&1 || die "请使用 root 运行（或安装 sudo），或设置 INSTALL_DIR/CONFIG_DIR/TANZHEN_DATA_DIR 到可写目录"
  SUDO="sudo"
fi

fetch() {
  _url="$1"; _dest="$2"
  # Timeouts matter more than they look: on a network that blackholes the
  # GitHub IPs, an unbounded curl hangs for minutes instead of failing fast
  # into the mirror fallback. speed-limit catches the nastier case where the
  # TCP connect succeeds but no data ever flows.
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --connect-timeout 10 --max-time 600 --speed-time 30 --speed-limit 8192 "$_url" -o "$_dest"
  elif command -v wget >/dev/null 2>&1; then
    wget -q --timeout=10 --tries=2 -O "$_dest" "$_url"
  else
    die "需要 curl 或 wget，请先安装其一"
  fi
}

try_fetch() {
  # try_fetch <url> <dest>: 0 on success, 1 on failure, without tripping set -e.
  if fetch "$1" "$2" 2>/dev/null; then
    [ -s "$2" ]
    return
  fi
  return 1
}

# dl downloads to a temp file first and installs it with the final mode, so the
# privileged destination is only ever written through $SUDO. The .new-in-place
# alternative fails on a root-owned INSTALL_DIR before sudo ever runs.
dl() {
  # dl <url> <final-dest> <mode>
  # Callers run in if/|| contexts where set -e is off, so the install result
  # must be checked explicitly: a swallowed ENOSPC here would surface much
  # later as a 404 from the hub's /releases/.
  _tmp="$(mktemp)"
  if try_fetch "$1" "$_tmp" && $SUDO install -m "$3" "$_tmp" "$2"; then
    rm -f "$_tmp"
    return 0
  fi
  rm -f "$_tmp"
  return 1
}

# dl_any tries the configured base first and falls back to GitHub Releases.
# A hub mirror (TANZHEN_BASE_URL=http://HUB/releases) only carries what that
# hub itself was given, so the fallback is what keeps a partially-stocked
# mirror usable.
GH_BASE="$REPO/releases/$VERSION/download"
dl_any() {
  # dl_any <filename> <final-dest> <mode>
  if [ "$BASE_URL" != "$GH_BASE" ] && dl "$BASE_URL/$1" "$2" "$3"; then
    return 0
  fi
  dl "$GH_BASE/$1" "$2" "$3"
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    armv7l|armv6l) echo arm ;;
    i386|i486|i586|i686) echo 386 ;;
    riscv64) echo riscv64 ;;
    loongarch64) echo loong64 ;;
    *) die "不支持的架构: $(uname -m)" ;;
  esac
}

detect_init() {
  if [ -n "$SUDO" ] || [ "$(id -u)" -eq 0 ]; then
    if [ -d /run/systemd/system ]; then
      INIT=systemd
    else
      INIT=nohup
    fi
  else
    # Non-root prefix install cannot write unit files under /etc.
    INIT=nohup
  fi
}

have_systemd() { [ "${INIT:-}" = systemd ] && command -v systemctl >/dev/null 2>&1; }

rand_pass() {
  # 24 url-safe characters from the kernel RNG.
  if [ -r /dev/urandom ]; then
    tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 24
  else
    # date+pid fallback; weaker but this path is unreachable on Linux.
    date +%s%N | md5sum | head -c 24
  fi
  printf '\n'
}

detect_public_ip() {
  _ip=""
  if command -v curl >/dev/null 2>&1; then
    _ip="$(curl -fsS --max-time 4 https://api.ipify.org 2>/dev/null || true)"
  elif command -v wget >/dev/null 2>&1; then
    _ip="$(wget -qO- --timeout=4 https://api.ipify.org 2>/dev/null || true)"
  fi
  if [ -z "$_ip" ]; then
    # iproute2 can answer without any egress call at all.
    _ip="$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}' | head -1 || true)"
  fi
  [ -n "$_ip" ] || _ip="127.0.0.1"
  echo "$_ip"
}

do_uninstall() {
  log "→ 卸载 Tanzhen Hub"
  detect_init
  if have_systemd; then
    $SUDO systemctl disable --now "$SERVICE" 2>/dev/null || true
    $SUDO rm -f "$UNIT_FILE"
    $SUDO systemctl daemon-reload 2>/dev/null || true
  else
    if [ -f "$PID_FILE" ]; then
      kill "$(cat "$PID_FILE")" 2>/dev/null || true
      $SUDO rm -f "$PID_FILE"
    fi
  fi
  $SUDO rm -f "$INSTALL_DIR/tanzhen-hub"
  # Only this hub's own env file, never the whole directory: /etc/tanzhen is
  # shared with the agent install (token, agent.env), and wiping it would stop
  # the hub from booting after the next reboot — no password, no start.
  $SUDO rm -f "$ENV_FILE"
  $SUDO rmdir "$CONFIG_DIR" 2>/dev/null || true
  $SUDO rm -f "$LOG_FILE"
  if [ "$PURGE" = 1 ]; then
    log "→ 删除数据目录 $DATA_DIR（含 SQLite 数据库与 agent 二进制）"
    $SUDO rm -rf "$DATA_DIR"
  else
    log "→ 保留数据目录 $DATA_DIR（加 --purge 可一并删除）"
  fi
  log "卸载完成。"
  exit 0
}

write_env_file() {
  # 0600: it holds the admin password in cleartext. Values are single-quote
  # escaped: a password with a space or a $ must survive sourcing by systemd
  # (and by install_nohup) as one value, not as two words or a substitution.
  _q() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"; }
  $SUDO mkdir -p "$CONFIG_DIR"
  $SUDO sh -c "cat > '$ENV_FILE'" <<EOT
# Tanzhen Hub 配置。由 install-hub.sh 生成；可直接编辑后重启服务。
ADMIN_USER=$(_q "$ADMIN_USER")
ADMIN_PASSWORD=$(_q "$ADMIN_PASSWORD")
PORT=$(_q "$HUB_PORT")
DATA_DIR=$(_q "$DATA_DIR")
RELEASES_DIR=$(_q "$RELEASES_DIR")
PUBLIC_URL=$(_q "$PUBLIC_URL")
TZ=$(_q "${TZ:-Asia/Shanghai}")
# 可选：节点离线 / 流量将满时 POST JSON（见 README「Webhook 告警」）
# WEBHOOK_URL=
# WEBHOOK_TRAFFIC_PCT=90
EOT
  $SUDO chmod 0600 "$ENV_FILE"
}

install_systemd() {
  $SUDO sh -c "cat > '$UNIT_FILE'" <<EOT
[Unit]
Description=Tanzhen monitoring hub
Documentation=$REPO
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=$ENV_FILE
ExecStart=$INSTALL_DIR/tanzhen-hub
Restart=always
RestartSec=3
LimitNOFILE=65535
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
# No PrivateTmp here on purpose: combined with ReadWritePaths it hides a
# DATA_DIR that lives under /tmp from the service's own mount namespace
# ("Failed to set up mount namespacing"), and the hub never writes to /tmp.
ReadWritePaths=$DATA_DIR

[Install]
WantedBy=multi-user.target
EOT
  $SUDO systemctl daemon-reload
  $SUDO systemctl enable --now "$SERVICE"
  log "✓ systemd 服务已启动: systemctl status $SERVICE"
}

install_nohup() {
  log "未检测到 systemd，使用 nohup 后台运行"
  # The env file is sourced inside the privileged shell, not before it: sudo's
  # env_reset would drop everything exported here, and the hub would start with
  # no ADMIN_PASSWORD and the default data dir.
  # Ensure log + pid dirs exist (PREFIX installs may put them under the workspace).
  $SUDO mkdir -p "$(dirname "$LOG_FILE")" "$(dirname "$PID_FILE")"
  $SUDO sh -c "set -a; . '$ENV_FILE'; set +a; nohup '$INSTALL_DIR/tanzhen-hub' >>'$LOG_FILE' 2>&1 & echo \$! > '$PID_FILE'"
  log "✓ 已后台运行 (pid $(cat "$PID_FILE"))，日志 $LOG_FILE"
}

main() {
  [ "$UNINSTALL" = 1 ] && do_uninstall

  # No re-exec through a file: under `curl | sh` there is no $0 to re-exec, and
  # downloading a second copy would double the traffic. Every privileged step
  # goes through $SUDO individually instead.
  ARCH="$(detect_arch)"
  detect_init
  log "探针主控一键部署 · arch=$ARCH init=$INIT version=$VERSION port=$HUB_PORT"

  $SUDO mkdir -p "$INSTALL_DIR" "$DATA_DIR" "$RELEASES_DIR"

  # ── 1. hub binary ────────────────────────────────────────────────────────
  log "→ 下载 hub: tanzhen-hub-linux-$ARCH"
  dl_any "tanzhen-hub-linux-$ARCH" "$INSTALL_DIR/tanzhen-hub" 0755 ||
    die "无法下载 hub 二进制。可设置 TANZHEN_BASE_URL 指向镜像。"

  # ── 2. agent binaries for target machines to fetch from this hub ─────────
  # Every platform the target-side installers can ask for, so a hub behind the
  # GFW never sends its agents to GitHub for the binary.
  for f in \
    tanzhen-agent-linux-amd64 tanzhen-agent-linux-arm64 tanzhen-agent-linux-arm \
    tanzhen-agent-linux-386 tanzhen-agent-linux-riscv64 tanzhen-agent-linux-loong64 \
    tanzhen-agent-darwin-amd64 tanzhen-agent-darwin-arm64 \
    tanzhen-agent-windows-amd64.exe tanzhen-agent-windows-arm64.exe
  do
    if dl_any "$f" "$RELEASES_DIR/$f" 0755; then
      :
    else
      log "· 跳过 $f"
    fi
  done

  # ── 3. admin password ────────────────────────────────────────────────────
  ADMIN_USER="${ADMIN_USER:-admin}"
  if [ -n "${ADMIN_PASSWORD:-}" ]; then
    :
  elif [ -t 0 ]; then
    printf '请设置管理后台密码 [回车随机生成]: '
    read -r ADMIN_PASSWORD || true
    if [ -z "$ADMIN_PASSWORD" ]; then ADMIN_PASSWORD="$(rand_pass)"; fi
  else
    ADMIN_PASSWORD="$(rand_pass)"
    GENERATED=1
  fi
  [ -n "$ADMIN_PASSWORD" ] || die "ADMIN_PASSWORD 不能为空"

  # ── 4. public URL ────────────────────────────────────────────────────────
  if [ -z "${PUBLIC_URL:-}" ]; then
    _ip="$(detect_public_ip)"
    PUBLIC_URL="http://$_ip:$HUB_PORT"
  fi

  # ── 5. service ───────────────────────────────────────────────────────────
  write_env_file
  # Stop the old unit before replacing the binary; a running process holds the
  # file busy on some systems and "Text file busy" kills the mv.
  if have_systemd; then
    $SUDO systemctl stop "$SERVICE" 2>/dev/null || true
    install_systemd
  else
    if [ -f "$PID_FILE" ]; then
      kill "$(cat "$PID_FILE")" 2>/dev/null || true
      $SUDO rm -f "$PID_FILE"
    fi
    install_nohup
  fi

  # ── 6. verify it answers before declaring success ────────────────────────
  _i=0
  while [ "$_i" -lt 20 ]; do
    if command -v curl >/dev/null 2>&1; then
      if curl -fsS --max-time 2 "http://127.0.0.1:$HUB_PORT/healthz" >/dev/null 2>&1; then break; fi
    elif command -v wget >/dev/null 2>&1; then
      if wget -qO /dev/null --timeout=2 "http://127.0.0.1:$HUB_PORT/healthz" 2>/dev/null; then break; fi
    fi
    _i=$((_i + 1))
    sleep 0.5
  done
  if [ "$_i" -ge 20 ]; then
    log "⚠ Hub 未在预期时间内响应，请检查日志："
    if have_systemd; then log "  journalctl -u $SERVICE -n 50 --no-pager"; else log "  tail -50 $LOG_FILE"; fi
    die "部署未完成"
  fi

  log ""
  log "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  log " 探针 Tanzhen 主控已就绪"
  log "  状态页:   $PUBLIC_URL/"
  log "  管理后台: $PUBLIC_URL/admin"
  log "  用户名:   $ADMIN_USER"
  if [ "${GENERATED:-0}" = 1 ]; then
    log "  密码:     $ADMIN_PASSWORD   ← 随机生成，请立即保存并修改"
  else
    log "  密码:     （使用你设置的 ADMIN_PASSWORD）"
  fi
  log ""
  log " 下一步：打开管理后台 → 新建节点 → 复制一键安装命令"
  log "       到任意 VPS 上执行即可接入。"
  log " 服务管理: systemctl {status,restart,stop} $SERVICE"
  log "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}

main "$@"
