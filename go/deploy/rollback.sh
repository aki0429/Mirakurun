#!/usr/bin/env bash
#
# Mirakurun を Go 版から Node 版へ戻すスクリプト (root 実行用)
#
# cutover.sh が保存したバックアップ (app-spec.json / dump.pm2) から、
# pm2 のエントリを元の Node 版に復元する。
#
# 使い方:
#   sudo ./rollback.sh                       # 直近のバックアップ (LATEST) から戻す
#   sudo ./rollback.sh --backup-dir /usr/local/var/backups/mirakurun-go/20261009-103000
#   sudo ./rollback.sh --list                # 利用できるバックアップの一覧
#   sudo ./rollback.sh --app-name Mirakurun --base-url http://127.0.0.1:40773
#   sudo ./rollback.sh --dry-run
#
set -euo pipefail

APP_NAME="${APP_NAME:-Mirakurun}"
BACKUP_ROOT="${BACKUP_ROOT:-/usr/local/var/backups/mirakurun-go}"
BACKUP_DIR="${BACKUP_DIR:-}"
BASE_URL="${BASE_URL:-http://127.0.0.1:40772}"
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-60}"
DRY_RUN=0

while [ $# -gt 0 ]; do
    case "$1" in
        --app-name)   APP_NAME="$2"; shift 2 ;;
        --backup-dir) BACKUP_DIR="$2"; shift 2 ;;
        --backup-root) BACKUP_ROOT="$2"; shift 2 ;;
        --base-url)   BASE_URL="$2"; shift 2 ;;
        --timeout)    HEALTH_TIMEOUT="$2"; shift 2 ;;
        --list)       ls -1t "$BACKUP_ROOT" 2>/dev/null | grep -v '^LATEST$' || echo "(バックアップはありません)"; exit 0 ;;
        --dry-run)    DRY_RUN=1; shift ;;
        -h|--help)    grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "unknown option: $1" >&2; exit 1 ;;
    esac
done

log()  { echo "[rollback] $*"; }
warn() { echo "[rollback] WARNING: $*" >&2; }
die()  { echo "[rollback] ERROR: $*" >&2; exit 1; }

run() {
    if [ "$DRY_RUN" = "1" ]; then
        echo "  + $*"
    else
        "$@"
    fi
}

# バックアップ先が指定されていない場合は LATEST を読む
if [ -z "$BACKUP_DIR" ] && [ -f "${BACKUP_ROOT}/LATEST" ]; then
    BACKUP_DIR="$(cat "${BACKUP_ROOT}/LATEST")"
fi
[ -n "$BACKUP_DIR" ] || die "no backup specified and ${BACKUP_ROOT}/LATEST not found (see --list)"
[ -f "${BACKUP_DIR}/app-spec.json" ] || die "app-spec.json not found in ${BACKUP_DIR}"

if [ "$DRY_RUN" != "1" ] && [ "$(id -u)" != "0" ]; then
    die "this script must be run as root"
fi
command -v pm2 >/dev/null || die "pm2 not found in PATH"

log "restoring '${APP_NAME}' from ${BACKUP_DIR}"

# 現在 (Go 版) のエントリを外してから、保存しておいた Node 版の仕様で起動し直す。
run pm2 stop "$APP_NAME" || true
sleep 2
run pm2 delete "$APP_NAME" || true
run pm2 start "${BACKUP_DIR}/app-spec.json"

# 復元の確認: Node 版が応答し、チャンネルがロードされていること。
if [ "$DRY_RUN" = "1" ]; then
    log "dry-run completed"
    exit 0
fi

waited=0
while [ "$waited" -lt "$HEALTH_TIMEOUT" ]; do
    if version_json="$(curl -sf -m 5 "${BASE_URL}/api/version" 2>/dev/null)"; then
        channels="$(curl -sf -m 5 "${BASE_URL}/api/channels" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))' 2>/dev/null || echo 0)"
        log "  health: version=${version_json} channels=${channels} (waited ${waited}s)"
        if [ "${channels}" -gt 0 ]; then
            pm2 save
            log "SUCCESS: '${APP_NAME}' is back on the Node implementation."
            exit 0
        fi
    fi
    sleep 2
    waited=$((waited + 2))
done

warn "restored app is not responding; pm2 dump.pm2 からの復元を試みます"
if [ -f "${BACKUP_DIR}/dump.pm2" ]; then
    pm2 kill
    cp -a "${BACKUP_DIR}/dump.pm2" /root/.pm2/dump.pm2
    pm2 resurrect
    pm2 save
    log "resurrected pm2 state from ${BACKUP_DIR}/dump.pm2 — pm2 list で状態を確認してください"
else
    die "rollback failed and no dump.pm2 in ${BACKUP_DIR}"
fi
