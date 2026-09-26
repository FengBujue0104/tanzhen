#!/bin/sh
# 探针 Tanzhen — 备份 SQLite 与外观资源。
#
# 用法:
#   ./scripts/backup.sh
#   DATA_DIR=/var/lib/tanzhen ./scripts/backup.sh
#   DATA_DIR=./data BACKUP_DIR=/var/backups/tanzhen ./scripts/backup.sh
#
# 有 sqlite3 时执行 .backup（Hub 开着也能得到已合并 WAL 的一致快照）。
# 没有 sqlite3 时复制 tanzhen.db 以及存在的 -wal / -shm；这种复制请先停 Hub。
# DATA_DIR/appearance/ 存在时一并打包。
#
# 产物: ${BACKUP_DIR}/tanzhen-YYYYmmdd-HHMMSS.tar.gz
#   内含 tanzhen.db；文件复制回退时还含 tanzhen.db-wal 与 tanzhen.db-shm；
#   有外观资源时含 appearance/。
#
# 恢复（先停 Hub）:
#   tar -xzf backups/tanzhen-YYYYmmdd-HHMMSS.tar.gz -C /tmp/tz-restore
#   cp -p /tmp/tz-restore/tanzhen.db "$DATA_DIR/tanzhen.db"
#   if [ -f /tmp/tz-restore/tanzhen.db-wal ]; then
#     cp -p /tmp/tz-restore/tanzhen.db-wal "$DATA_DIR/tanzhen.db-wal"
#     [ -f /tmp/tz-restore/tanzhen.db-shm ] && cp -p /tmp/tz-restore/tanzhen.db-shm "$DATA_DIR/tanzhen.db-shm"
#   else
#     # .backup 已把 WAL 合并进 tanzhen.db。删掉目录里旧的 wal/shm，
#     # 避免下次打开时重放，盖掉刚恢复的库。
#     rm -f "$DATA_DIR/tanzhen.db-wal" "$DATA_DIR/tanzhen.db-shm"
#   fi
#   if [ -d /tmp/tz-restore/appearance ]; then
#     rm -rf "$DATA_DIR/appearance"
#     cp -a /tmp/tz-restore/appearance "$DATA_DIR/appearance"
#   fi
# 然后启动 Hub。
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
DATA_DIR="${DATA_DIR:-$ROOT/data}"
BACKUP_DIR="${BACKUP_DIR:-$ROOT/backups}"
DB="$DATA_DIR/tanzhen.db"

if [ ! -f "$DB" ]; then
  echo "backup: database not found: $DB" >&2
  exit 1
fi

mkdir -p "$BACKUP_DIR"
STAMP="$(date +%Y%m%d-%H%M%S)"
STAGE=""
cleanup() {
  if [ -n "${STAGE}" ] && [ -d "$STAGE" ]; then
    rm -rf "$STAGE"
  fi
}
trap cleanup EXIT
STAGE="$(mktemp -d "${TMPDIR:-/tmp}/tanzhen-backup.XXXXXX")"

# sqlite dot-command 用单引号包裹路径；路径里的单引号改成 ''。
sql_quote() {
  printf "'%s'" "$(printf '%s' "$1" | sed "s/'/''/g")"
}

if command -v sqlite3 >/dev/null 2>&1 \
  && sqlite3 "$DB" ".backup $(sql_quote "$STAGE/tanzhen.db")"
then
  echo "backup: sqlite3 .backup"
else
  rm -f "$STAGE/tanzhen.db"
  echo "backup: sqlite3 unavailable or .backup failed; copying db + wal/shm (stop the hub first for a consistent copy)" >&2
  cp -p "$DB" "$STAGE/tanzhen.db"
  if [ -f "${DB}-wal" ]; then
    cp -p "${DB}-wal" "$STAGE/tanzhen.db-wal"
  fi
  if [ -f "${DB}-shm" ]; then
    cp -p "${DB}-shm" "$STAGE/tanzhen.db-shm"
  fi
fi

if [ -d "$DATA_DIR/appearance" ]; then
  cp -a "$DATA_DIR/appearance" "$STAGE/appearance"
fi

OUT="$BACKUP_DIR/tanzhen-$STAMP.tar.gz"
if [ -e "$OUT" ]; then
  OUT="$BACKUP_DIR/tanzhen-$STAMP-$$.tar.gz"
fi
tar -C "$STAGE" -czf "$OUT" .
echo "backup: wrote $OUT"
