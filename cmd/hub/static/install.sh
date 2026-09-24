#!/bin/sh
# 探针 Tanzhen · Linux 一键扎针
#
# 用法:
#   curl -fsSL 'http://HUB/install.sh?hub=http://HUB&token=TOKEN' | sh
#   curl -fsSL http://HUB/install.sh | sh -s -- --hub http://HUB --token TOKEN
#
# POSIX sh on purpose: Alpine, OpenWrt and most minimal images ship busybox ash
# and no bash, so `[[ ]]` and `pipefail` are not available. The hub injects
# HUB_URL/TOKEN when the query-string form is used.
set -eu

HUB_URL="${HUB_URL:-}"
TOKEN="${TOKEN:-}"
VERSION="${TANZHEN_VERSION:-latest}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
CONFIG_DIR="${CONFIG_DIR:-/etc/tanzhen}"
TOKEN_FILE="$CONFIG_DIR/token"
ENV_FILE="$CONFIG_DIR/agent.env"
LOG_FILE="${LOG_FILE:-/var/log/tanzhen-agent.log}"
SERVICE_NAME="tanzhen-agent"
REPO="https://github.com/FengBujue0104/tanzhen"
UNINSTALL=0

usage() {
  cat <<EOT
用法: $0 --hub <HUB_URL> --token <TOKEN>
或:   curl -fsSL 'http://HUB/install.sh?hub=http://HUB&token=TOKEN' | sh

卸载: ... | sh -s -- --uninstall
  移除服务、二进制与 token（凭证不残留在盘上）

可选环境变量:
  TANZHEN_VERSION   拉取的版本标签 (默认 latest)
  TANZHEN_INTERVAL  上报间隔 (默认 2s)
  TANZHEN_PROBE_EVERY / TANZHEN_PROBE_COUNT  三网探测间隔 / 每次发包数
  INSTALL_DIR / CONFIG_DIR  安装与配置目录
EOT
  exit 1
}

while [ $# -gt 0 ]; do
  case "$1" in
    --hub) [ $# -ge 2 ] || usage; HUB_URL="$2"; shift 2 ;;
    --token) [ $# -ge 2 ] || usage; TOKEN="$2"; shift 2 ;;
    --uninstall) UNINSTALL=1; shift ;;
    -h|--help) usage ;;
    *) echo "未知参数: $1" >&2; usage ;;
  esac
done

if [ "$UNINSTALL" != 1 ]; then
  HUB_URL="${HUB_URL%/}"
  [ -n "$HUB_URL" ] && [ -n "$TOKEN" ] || usage
fi

do_uninstall() {
  log "→ 卸载 Tanzhen Agent"
  detect_os
  detect_init
  case "$INIT" in
    systemd)
      systemctl disable --now "$SERVICE_NAME" 2>/dev/null || true
      rm -f "/etc/systemd/system/$SERVICE_NAME.service"
      systemctl daemon-reload 2>/dev/null || true
      ;;
    procd|openrc)
      if [ -x "/etc/init.d/$SERVICE_NAME" ]; then
        "/etc/init.d/$SERVICE_NAME" stop 2>/dev/null || true
        "/etc/init.d/$SERVICE_NAME" disable 2>/dev/null || true
        rc-update del "$SERVICE_NAME" default 2>/dev/null || true
        rm -f "/etc/init.d/$SERVICE_NAME"
      fi
      ;;
    *)
      if [ -f /var/run/tanzhen-agent.pid ]; then
        kill "$(cat /var/run/tanzhen-agent.pid)" 2>/dev/null || true
        rm -f /var/run/tanzhen-agent.pid
      fi
      ;;
  esac
  pkill -f "$INSTALL_DIR/tanzhen-agent" 2>/dev/null || true
  rm -f "$INSTALL_DIR/tanzhen-agent"
  rm -f "$LOG_FILE"
  # Remove only this agent's own files: /etc/tanzhen is shared with a hub
  # install on the same box, whose env must survive an agent uninstall. The
  # token is a credential, so it goes on every uninstall, purge or not.
  rm -f "$TOKEN_FILE" "$ENV_FILE"
  rmdir "$CONFIG_DIR" 2>/dev/null || true
  log "卸载完成。可在 Hub 管理后台删除该节点。"
  exit 0
}

log() { printf '%s\n' "$*"; }
die() { printf '错误: %s\n' "$*" >&2; exit 1; }

need_root() {
  [ "$(id -u)" -eq 0 ] || die "请使用 root 运行（或 sudo）"
}

# curl or wget, whichever the image happens to ship.
fetch() {
  _url="$1"; _dest="$2"
  # Bounded so a blackholed GitHub fails fast into the next source instead of
  # hanging the installer; speed-limit covers connect-succeeds-but-no-data.
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --connect-timeout 10 --max-time 600 --speed-time 30 --speed-limit 8192 "$_url" -o "$_dest"
  elif command -v wget >/dev/null 2>&1; then
    wget -q --timeout=10 --tries=2 -O "$_dest" "$_url"
  else
    die "需要 curl 或 wget，请先安装其一"
  fi
}

detect_os() {
  OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
  case "$OS" in
    linux) ;;
    darwin) log "检测到 macOS，将使用 nohup 方式运行（无 launchd 服务）" ;;
    *) die "不支持的系统: $OS（Windows 请用 install.ps1）" ;;
  esac
  ARCH_RAW="$(uname -m)"
  case "$ARCH_RAW" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    armv7l|armv6l) ARCH=arm ;;
    i386|i486|i586|i686) ARCH=386 ;;
    riscv64) ARCH=riscv64 ;;
    loongarch64) ARCH=loong64 ;;
    *) die "不支持的架构: $ARCH_RAW" ;;
  esac
}

# Ordered most-preferred first. A container can have systemctl installed without
# systemd being PID 1, so /run/systemd/system is the authoritative signal.
detect_init() {
  if [ "$OS" = "darwin" ]; then
    # macOS has no systemd; launchd needs a plist in /Library, which is a lot
    # of surface for a monitor. A supervised nohup is honest and easy to
    # remove; the agent is crash-only anyway (state lives on the hub).
    INIT=nohup
  elif [ -d /run/systemd/system ]; then
    INIT=systemd
  elif [ -x /sbin/procd ] || command -v procd >/dev/null 2>&1 || [ -f /etc/openwrt_release ]; then
    INIT=procd
  elif command -v rc-service >/dev/null 2>&1 || [ -d /etc/init.d ]; then
    INIT=openrc
  else
    INIT=none
  fi
}

download_agent() {
  # Linux and macOS agents are plain GOOS-GOARCH names; Windows is a separate
  # script (install.ps1), so there is no .exe case here.
  bin="tanzhen-agent-$OS-$ARCH"
  url="$HUB_URL/releases/$bin"
  mkdir -p "$INSTALL_DIR"
  log "→ 从 Hub 下载 agent: $url"
  if fetch "$url" "$INSTALL_DIR/tanzhen-agent" 2>/dev/null; then
    chmod 0755 "$INSTALL_DIR/tanzhen-agent"
    return 0
  fi
  log "→ Hub 未提供二进制，尝试 GitHub Releases"
  gh="$REPO/releases/$VERSION/download/$bin"
  if fetch "$gh" "$INSTALL_DIR/tanzhen-agent" 2>/dev/null; then
    chmod 0755 "$INSTALL_DIR/tanzhen-agent"
    return 0
  fi
  if command -v go >/dev/null 2>&1; then
    log "→ 使用本地 Go 工具链编译"
    tmp="$(mktemp -d)"
    if fetch "$REPO/archive/refs/tags/$VERSION.tar.gz" "$tmp/src.tar.gz" 2>/dev/null ||
       fetch "$REPO/archive/refs/heads/main.tar.gz" "$tmp/src.tar.gz" 2>/dev/null; then
      tar -xzf "$tmp/src.tar.gz" -C "$tmp" --strip-components=1
      (cd "$tmp" && CGO_ENABLED=0 GOOS="$OS" GOARCH="$ARCH" go build -trimpath -ldflags="-s -w" \
        -o "$INSTALL_DIR/tanzhen-agent" ./cmd/agent)
      chmod 0755 "$INSTALL_DIR/tanzhen-agent"
      rm -rf "$tmp"
      return 0
    fi
    rm -rf "$tmp"
  fi
  die "无法获取 agent 二进制：请把交叉编译好的 $bin 放到 Hub 的 releases/ 目录，或在目标机安装 Go"
}

# The token goes in its own 0600 file and is passed with --token-file, so it never
# appears in the process table or in a shell's history line.
write_config() {
  mkdir -p "$CONFIG_DIR"
  chmod 0700 "$CONFIG_DIR"
  printf '%s\n' "$TOKEN" > "$TOKEN_FILE"
  chmod 0600 "$TOKEN_FILE"
  {
    printf 'HUB_URL=%s\n' "$HUB_URL"
    printf 'TANZHEN_INTERVAL=%s\n' "${TANZHEN_INTERVAL:-2s}"
    printf 'TANZHEN_PROBE_EVERY=%s\n' "${TANZHEN_PROBE_EVERY:-30s}"
    printf 'TANZHEN_PROBE_COUNT=%s\n' "${TANZHEN_PROBE_COUNT:-4}"
  } > "$ENV_FILE"
  chmod 0600 "$ENV_FILE"
}

agent_cmdline() {
  # hub URL and tunables on the command line; the secret stays in the file.
  printf '%s --hub %s --token-file %s' "$INSTALL_DIR/tanzhen-agent" "$HUB_URL" "$TOKEN_FILE"
}

install_systemd() {
  cat > "/etc/systemd/system/$SERVICE_NAME.service" <<EOT
[Unit]
Description=Tanzhen monitoring agent
Documentation=$REPO
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=$ENV_FILE
ExecStart=$INSTALL_DIR/tanzhen-agent --hub \${HUB_URL} --token-file $TOKEN_FILE --interval \${TANZHEN_INTERVAL} --probe-every \${TANZHEN_PROBE_EVERY} --probe-count \${TANZHEN_PROBE_COUNT}
Restart=always
RestartSec=3
LimitNOFILE=65535
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOT
  systemctl daemon-reload
  systemctl enable --now "$SERVICE_NAME"
  log "✓ systemd 服务已启动: systemctl status $SERVICE_NAME"
}

install_openrc() {
  cat > "/etc/init.d/$SERVICE_NAME" <<EOT
#!/sbin/openrc-run
name="tanzhen-agent"
description="Tanzhen monitoring agent"
command="$INSTALL_DIR/tanzhen-agent"
command_args="--hub $HUB_URL --token-file $TOKEN_FILE"
pidfile="/run/\${RC_SVCNAME}.pid"
command_background=true
output_logger="true"

depend() {
  need net
  after firewall
}

start_pre() {
  . '$ENV_FILE' 2>/dev/null || true
  TANZHEN_INTERVAL="\${TANZHEN_INTERVAL:-2s}"
  TANZHEN_PROBE_EVERY="\${TANZHEN_PROBE_EVERY:-30s}"
  TANZHEN_PROBE_COUNT="\${TANZHEN_PROBE_COUNT:-4}"
  command_args="--hub $HUB_URL --token-file $TOKEN_FILE --interval \$TANZHEN_INTERVAL --probe-every \$TANZHEN_PROBE_EVERY --probe-count \$TANZHEN_PROBE_COUNT"
}
EOT
  chmod 0755 "/etc/init.d/$SERVICE_NAME"
  rc-update add "$SERVICE_NAME" default 2>/dev/null || true
  rc-service "$SERVICE_NAME" restart 2>/dev/null || rc-service "$SERVICE_NAME" start
  log "✓ OpenRC 服务已启动: rc-service $SERVICE_NAME status"
}

# OpenWrt's procd: no pidfiles, supervised by init.
install_procd() {
  cat > "/etc/init.d/$SERVICE_NAME" <<EOT
#!/bin/sh /etc/rc.common
START=99
USE_PROCD=1

start_service() {
  . '$ENV_FILE' 2>/dev/null || true
  procd_open_instance
  procd_set_param command $INSTALL_DIR/tanzhen-agent --hub $HUB_URL --token-file $TOKEN_FILE --interval \${TANZHEN_INTERVAL:-2s} --probe-every \${TANZHEN_PROBE_EVERY:-30s} --probe-count \${TANZHEN_PROBE_COUNT:-4}
  procd_set_param respawn
  procd_set_param stdout 1
  procd_set_param stderr 1
  procd_close_instance
}

stop_service() {
  :
}

service_stopped() {
  echo "$SERVICE_NAME stopped"
}
EOT
  chmod 0755 "/etc/init.d/$SERVICE_NAME"
  "/etc/init.d/$SERVICE_NAME" enable 2>/dev/null || true
  "/etc/init.d/$SERVICE_NAME" restart
  log "✓ procd 服务已启动: /etc/init.d/$SERVICE_NAME status"
}

# Last resort for containers and exotic init systems: a supervised nohup.
install_nohup() {
  log "未检测到 systemd/OpenRC/procd，使用 nohup 后台运行"
  if [ -f /var/run/tanzhen-agent.pid ]; then
    kill "$(cat /var/run/tanzhen-agent.pid)" 2>/dev/null || true
    rm -f /var/run/tanzhen-agent.pid
  fi
  # Source the tunables so this path honors TANZHEN_INTERVAL / PROBE_* exactly
  # like the systemd unit does — the agent takes them as flags, not env.
  set -a
  # shellcheck disable=SC1090
  . "$ENV_FILE"
  set +a
  nohup "$INSTALL_DIR/tanzhen-agent" --hub "$HUB_URL" --token-file "$TOKEN_FILE" \
    --interval "${TANZHEN_INTERVAL:-2s}" --probe-every "${TANZHEN_PROBE_EVERY:-30s}" \
    --probe-count "${TANZHEN_PROBE_COUNT:-4}" \
    >>"$LOG_FILE" 2>&1 &
  echo $! > /var/run/tanzhen-agent.pid
  log "✓ 已后台运行 (pid $(cat /var/run/tanzhen-agent.pid))，日志 $LOG_FILE"
}

main() {
  need_root
  [ "$UNINSTALL" = 1 ] && do_uninstall
  detect_os
  detect_init
  log "探针一键扎针 · arch=$ARCH init=$INIT hub=$HUB_URL"
  download_agent
  write_config
  case "$INIT" in
    systemd) install_systemd ;;
    procd)   install_procd ;;
    openrc)  install_openrc ;;
    *)       install_nohup ;;
  esac
  log "完成。刷新状态页即可看到节点上线。"
}

main
