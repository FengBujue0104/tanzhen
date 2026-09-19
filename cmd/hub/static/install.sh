#!/usr/bin/env bash
# 探针 Tanzhen · Linux 一键扎针
# 用法: curl -fsSL http://HUB/install.sh | bash -s -- --hub http://HUB --token TOKEN
set -euo pipefail

HUB_URL=""
TOKEN=""
VERSION="${TANZHEN_VERSION:-latest}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
CONFIG_DIR="${CONFIG_DIR:-/etc/tanzhen}"
SERVICE_NAME="tanzhen-agent"

usage() {
  echo "Usage: $0 --hub <HUB_URL> --token <TOKEN>"
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --hub) HUB_URL="${2:-}"; shift 2 ;;
    --token) TOKEN="${2:-}"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "Unknown arg: $1"; usage ;;
  esac
done

HUB_URL="${HUB_URL%/}"
[[ -n "$HUB_URL" && -n "$TOKEN" ]] || usage

need_root() {
  if [[ "$(id -u)" -ne 0 ]]; then
    echo "请使用 root 运行（或 sudo）"
    exit 1
  fi
}

detect_os() {
  OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
  ARCH="$(uname -m)"
  case "$ARCH" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    armv7l) ARCH=arm ;;
    *) echo "不支持的架构: $ARCH"; exit 1 ;;
  esac
  [[ "$OS" == "linux" ]] || { echo "此脚本仅支持 Linux"; exit 1; }
}

detect_init() {
  if [[ -d /run/systemd/system ]] || command -v systemctl >/dev/null 2>&1; then
    INIT=systemd
  elif command -v rc-service >/dev/null 2>&1 || [[ -d /etc/init.d ]]; then
    INIT=openrc
  else
    INIT=none
  fi
}

download_agent() {
  local url="$HUB_URL/releases/tanzhen-agent-linux-${ARCH}"
  local dest="$INSTALL_DIR/tanzhen-agent"
  mkdir -p "$INSTALL_DIR"
  echo "→ 尝试从 Hub 下载: $url"
  if curl -fsSL "$url" -o "$dest" 2>/dev/null; then
    chmod +x "$dest"
    return 0
  fi
  # GitHub releases fallback
  local gh="https://github.com/FengBujue0104/tanzhen/releases/${VERSION}/download/tanzhen-agent-linux-${ARCH}"
  echo "→ Hub 无二进制，尝试 GitHub: $gh"
  if curl -fsSL "$gh" -o "$dest" 2>/dev/null; then
    chmod +x "$dest"
    return 0
  fi
  # Build from source if go available
  if command -v go >/dev/null 2>&1; then
    echo "→ 使用本地 Go 编译 agent..."
    local tmp
    tmp="$(mktemp -d)"
    if curl -fsSL "https://github.com/FengBujue0104/tanzhen/archive/refs/heads/main.tar.gz" | tar -xz -C "$tmp" --strip-components=1 2>/dev/null; then
      (cd "$tmp" && CGO_ENABLED=0 go build -ldflags="-s -w" -o "$dest" ./cmd/agent)
      chmod +x "$dest"
      rm -rf "$tmp"
      return 0
    fi
    rm -rf "$tmp"
  fi
  echo "无法获取 agent 二进制。请手动交叉编译后放到 Hub 的 releases/ 或设置 PATH 中的 go。"
  exit 1
}

write_config() {
  mkdir -p "$CONFIG_DIR"
  cat > "$CONFIG_DIR/agent.env" <<EOT
HUB_URL=$HUB_URL
TOKEN=$TOKEN
EOT
  chmod 600 "$CONFIG_DIR/agent.env"
}

install_systemd() {
  cat > /etc/systemd/system/${SERVICE_NAME}.service <<EOT
[Unit]
Description=Tanzhen monitoring agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=$CONFIG_DIR/agent.env
ExecStart=$INSTALL_DIR/tanzhen-agent --hub \${HUB_URL} --token \${TOKEN}
Restart=always
RestartSec=3
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOT
  systemctl daemon-reload
  systemctl enable --now ${SERVICE_NAME}
  echo "✓ systemd 服务已启动: systemctl status ${SERVICE_NAME}"
}

install_openrc() {
  cat > /etc/init.d/${SERVICE_NAME} <<EOT
#!/sbin/openrc-run
name="tanzhen-agent"
command="$INSTALL_DIR/tanzhen-agent"
command_args="--hub \${HUB_URL} --token \${TOKEN}"
pidfile="/run/\${RC_SVCNAME}.pid"
command_background=true

depend() {
  need net
}

start_pre() {
  # shellcheck disable=SC1091
  . $CONFIG_DIR/agent.env
  export HUB_URL TOKEN
  command_args="--hub \${HUB_URL} --token \${TOKEN}"
}
EOT
  chmod +x /etc/init.d/${SERVICE_NAME}
  # openrc env
  if [[ -d /etc/conf.d ]]; then
    cat > /etc/conf.d/${SERVICE_NAME} <<EOT
HUB_URL="$HUB_URL"
TOKEN="$TOKEN"
EOT
  fi
  rc-update add ${SERVICE_NAME} default 2>/dev/null || true
  rc-service ${SERVICE_NAME} restart || rc-service ${SERVICE_NAME} start
  echo "✓ OpenRC 服务已启动"
}

install_cron_fallback() {
  echo "未检测到 systemd/OpenRC，使用 nohup 后台运行"
  pkill -f "$INSTALL_DIR/tanzhen-agent" 2>/dev/null || true
  # shellcheck disable=SC1090
  set -a; source "$CONFIG_DIR/agent.env"; set +a
  nohup "$INSTALL_DIR/tanzhen-agent" --hub "$HUB_URL" --token "$TOKEN" \
    >/var/log/tanzhen-agent.log 2>&1 &
  echo "✓ 已后台启动，日志 /var/log/tanzhen-agent.log"
}

main() {
  need_root
  detect_os
  detect_init
  echo "探针一键扎针 · arch=$ARCH init=$INIT hub=$HUB_URL"
  download_agent
  write_config
  case "$INIT" in
    systemd) install_systemd ;;
    openrc) install_openrc ;;
    *) install_cron_fallback ;;
  esac
  echo "完成。在状态页查看节点是否上线。"
}

main
