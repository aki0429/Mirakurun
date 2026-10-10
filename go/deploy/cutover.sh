#!/usr/bin/env bash
#
# Mirakurun を Node 版から Go 版へ切り替えるスクリプト (root 実行用)
#
# 実行順序:
#   1. 事前確認   : 本番設定を使って Go バイナリが設定・DB を読めるか (本番ファイルには書き込まない)
#   2. バックアップ: pm2 の状態 (dump.pm2) と対象アプリの起動仕様 (app-spec.json)・設定ファイル
#   3. 差し替え   : pm2 のエントリを Node 版から Go バイナリへ置き換える
#   4. ヘルスチェック: /api/version, /api/channels, /api/services, /api/tuners を検証
#   5. 失敗時     : 自動で Node 版へロールバックし、非 0 で終了する
#
# 使い方:
#   sudo ./cutover.sh --app-name Mirakurun                 # 既定 (サーバー本体)
#   sudo ./cutover.sh --app-name Mirakurun --base-url http://127.0.0.1:40773
#   sudo ./cutover.sh --dry-run                            # 実行内容の確認のみ
#
# 注意: 2 インスタンス (本体 / -q1ud) を運用している場合は、1 インスタンスずつ実行する。
#
set -euo pipefail

# ---- 既定値 (環境変数または引数で上書きできる) ----------------------------
APP_NAME="${APP_NAME:-Mirakurun}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/lib/mirakurun-go}"
BACKUP_ROOT="${BACKUP_ROOT:-/usr/local/var/backups/mirakurun-go}"
BASE_URL="${BASE_URL:-http://127.0.0.1:40772}"
CONFIG_DIR="${CONFIG_DIR:-/usr/local/etc/mirakurun}"
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-60}"
DRY_RUN=0
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BINARY="${BINARY:-${SCRIPT_DIR}/mirakurun-go-linux-amd64}"

# 設定・DB の既定パス (Go バイナリ自身と同じ既定値を、pm2 の env として明示的に渡す)
SERVER_CONFIG_PATH="${SERVER_CONFIG_PATH:-${CONFIG_DIR}/server.yml}"
TUNERS_CONFIG_PATH="${TUNERS_CONFIG_PATH:-${CONFIG_DIR}/tuners.yml}"
CHANNELS_CONFIG_PATH="${CHANNELS_CONFIG_PATH:-${CONFIG_DIR}/channels.yml}"
SERVICES_DB_PATH="${SERVICES_DB_PATH:-/usr/local/var/db/mirakurun/services.json}"
PROGRAMS_DB_PATH="${PROGRAMS_DB_PATH:-/usr/local/var/db/mirakurun/programs.json}"
LOGO_DATA_DIR_PATH="${LOGO_DATA_DIR_PATH:-/usr/local/var/db/mirakurun/logo-data}"

usage() {
    sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
    exit "${1:-0}"
}

while [ $# -gt 0 ]; do
    case "$1" in
        --app-name)   APP_NAME="$2"; shift 2 ;;
        --install-dir) INSTALL_DIR="$2"; shift 2 ;;
        --backup-dir) BACKUP_ROOT="$2"; shift 2 ;;
        --base-url)   BASE_URL="$2"; shift 2 ;;
        --config-dir) CONFIG_DIR="$2"; shift 2 ;;
        --binary)     BINARY="$2"; shift 2 ;;
        --timeout)    HEALTH_TIMEOUT="$2"; shift 2 ;;
        --dry-run)    DRY_RUN=1; shift ;;
        -h|--help)    usage 0 ;;
        *) echo "unknown option: $1" >&2; usage 1 ;;
    esac
done

log()  { echo "[cutover] $*"; }
warn() { echo "[cutover] WARNING: $*" >&2; }
die()  { echo "[cutover] ERROR: $*" >&2; exit 1; }

run() {
    # dry-run のときはコマンドを表示するだけで実行しない
    if [ "$DRY_RUN" = "1" ]; then
        echo "  + $*"
    else
        "$@"
    fi
}

# ---- 0. 前提確認 ----------------------------------------------------------
log "app=${APP_NAME} base_url=${BASE_URL} binary=${BINARY}"
if [ "$DRY_RUN" != "1" ] && [ "$(id -u)" != "0" ]; then
    die "this script must be run as root (pm2 is owned by the root profile)"
fi
command -v pm2 >/dev/null || die "pm2 not found in PATH"
command -v curl >/dev/null || die "curl not found in PATH"
[ -f "$BINARY" ] || die "binary not found: ${BINARY} (build it with: CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o mirakurun-go-linux-amd64 ./cmd/mirakurun-go)"
# scp でコピーすると実行ビットが落ちることがあるため、ここで補う
if [ ! -x "$BINARY" ]; then
    chmod +x "$BINARY" 2>/dev/null || true
fi
[ -x "$BINARY" ] || die "binary is not executable: ${BINARY} (chmod +x it after copying)"
[ -f "$SERVER_CONFIG_PATH" ] || die "server config not found: ${SERVER_CONFIG_PATH}"

# 事前確認: 一時ディレクトリにコピーした設定で Go バイナリを起動し、
# 設定ファイルと DB を読めることを確認する (本番の設定・DB には一切書き込まない)。
TMP="$(mktemp -d)"
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT

log "step 1/5: preflight (reading production config from a copy; no writes to production)"
mkdir -p "$TMP/config" "$TMP/db"
cp -a "$SERVER_CONFIG_PATH" "$TMP/config/server.yml"
cp -a "$TUNERS_CONFIG_PATH" "$TMP/config/tuners.yml"
cp -a "$CHANNELS_CONFIG_PATH" "$TMP/config/channels.yml"
# DB は空ファイルを渡す (設定の解釈とチューナー定義の読み込みだけを検証する)
: > "$TMP/db/services.json"
: > "$TMP/db/programs.json"
mkdir -p "$TMP/db/logo-data"
# 本番ファイルには触れないため、dry-run でも実行する (設定が読めるかどうかはここで分かる)。
if ! SETUP=true \
    SERVER_CONFIG_PATH="$TMP/config/server.yml" \
    TUNERS_CONFIG_PATH="$TMP/config/tuners.yml" \
    CHANNELS_CONFIG_PATH="$TMP/config/channels.yml" \
    SERVICES_DB_PATH="$TMP/db/services.json" \
    PROGRAMS_DB_PATH="$TMP/db/programs.json" \
    LOGO_DATA_DIR_PATH="$TMP/db/logo-data" \
    "$BINARY" >"$TMP/preflight.log" 2>&1; then
    cat "$TMP/preflight.log" >&2
    die "preflight failed: the binary could not load the production config"
fi
if ! grep -q "setup is done." "$TMP/preflight.log"; then
    cat "$TMP/preflight.log" >&2
    die "preflight failed: unexpected output (see above)"
fi
log "  preflight OK"

# ---- 1. バックアップ -------------------------------------------------------
TS="$(date +%Y%m%d-%H%M%S)"
BACKUP_DIR="${BACKUP_ROOT}/${TS}"
log "step 2/5: backup -> ${BACKUP_DIR}"
run mkdir -p "$BACKUP_DIR"

# pm2 の現在のアプリ一覧を保存し、対象アプリの起動仕様を抽出する。
# 復元はこの JSON を `pm2 start` に渡すだけで済むようにする。
if ! pm2 jlist > "$TMP/jlist.json" 2>"$TMP/jlist.err"; then
    cat "$TMP/jlist.err" >&2
    die "pm2 jlist failed (run this script with sudo, or check the pm2 profile)"
fi
APP_JSON="$(python3 - "$TMP/jlist.json" "$APP_NAME" <<'PY'
import json, sys
apps = json.load(open(sys.argv[1], encoding='utf-8'))
name = sys.argv[2]
hit = [a for a in apps if a.get('name') == name]
if not hit:
    # 名前が一致しない場合は script パスに mirakurun を含むアプリを探す
    hit = [a for a in apps if 'mirakurun' in (a.get('pm2_env', {}).get('pm_exec_path') or '').lower()]
if len(hit) != 1:
    print(f'ERROR: expected exactly 1 pm2 app for {name!r}, found {len(hit)}', file=sys.stderr)
    for a in apps:
        print(f"  name={a.get('name')} script={a.get('pm2_env', {}).get('pm_exec_path')}", file=sys.stderr)
    sys.exit(1)
e = hit[0]['pm2_env']
spec = {
    'apps': [{
        'name': hit[0]['name'],
        'script': e.get('pm_exec_path'),
        'cwd': e.get('pm_cwd'),
        'args': e.get('args'),
        'interpreter': e.get('exec_interpreter') or 'node',
        'exec_mode': 'fork',
        'env': {k: v for k, v in e.get('env', {}).items()
                if isinstance(v, str) and k in (
                    'SERVER_CONFIG_PATH', 'TUNERS_CONFIG_PATH', 'CHANNELS_CONFIG_PATH',
                    'SERVICES_DB_PATH', 'PROGRAMS_DB_PATH', 'LOGO_DATA_DIR_PATH', 'NODE_ENV')},
    }]
}
print(json.dumps(spec, ensure_ascii=False, indent=2))
PY
)" || die "could not identify the pm2 app to replace"
if [ "$DRY_RUN" = "1" ]; then
    log "  would save app spec: $(echo "$APP_JSON" | python3 -c 'import json,sys; a=json.load(sys.stdin)["apps"][0]; print(a["name"], "<-", a["script"])')"
else
    echo "$APP_JSON" > "$BACKUP_DIR/app-spec.json"
    log "  saved app spec: $(echo "$APP_JSON" | python3 -c 'import json,sys; a=json.load(sys.stdin)["apps"][0]; print(a["name"], "<-", a["script"])')"
    log "  saved app spec -> ${BACKUP_DIR}/app-spec.json"
fi

run cp -a "$SERVER_CONFIG_PATH" "$TUNERS_CONFIG_PATH" "$CHANNELS_CONFIG_PATH" "$BACKUP_DIR/"
run pm2 save
if [ "$DRY_RUN" != "1" ]; then
    # pm2 save が書き出した dump を控える (pm2 resurrect による復元用)
    for d in /root/.pm2/dump.pm2 "$HOME/.pm2/dump.pm2"; do
        [ -f "$d" ] && cp -a "$d" "$BACKUP_DIR/dump.pm2" && break
    done
    [ -f "$BACKUP_DIR/dump.pm2" ] || warn "pm2 dump.pm2 を控えられませんでした (app-spec.json からの復元は可能)"
    # 以降のロールバックで使うため、バックアップ先を記録する
    echo "$BACKUP_DIR" > "${BACKUP_ROOT}/LATEST"
fi

# ---- 2. バイナリの配置 -----------------------------------------------------
log "step 3/5: install binary -> ${INSTALL_DIR}/mirakurun-go"
run mkdir -p "$INSTALL_DIR"
run install -m 0755 "$BINARY" "$INSTALL_DIR/mirakurun-go"

# Go バイナリを pm2 で動かすための起動仕様を作る。
# interpreter を none にして Node でラップさせない (これが無いと起動しない)。
GO_SPEC="${BACKUP_DIR}/go-spec.json"
write_go_spec() {
cat > "$GO_SPEC" <<JSON
{
  "apps": [
    {
      "name": "${APP_NAME}",
      "script": "${INSTALL_DIR}/mirakurun-go",
      "interpreter": "none",
      "exec_mode": "fork",
      "kill_timeout": 15000,
      "env": {
        "SERVER_CONFIG_PATH": "${SERVER_CONFIG_PATH}",
        "TUNERS_CONFIG_PATH": "${TUNERS_CONFIG_PATH}",
        "CHANNELS_CONFIG_PATH": "${CHANNELS_CONFIG_PATH}",
        "SERVICES_DB_PATH": "${SERVICES_DB_PATH}",
        "PROGRAMS_DB_PATH": "${PROGRAMS_DB_PATH}",
        "LOGO_DATA_DIR_PATH": "${LOGO_DATA_DIR_PATH}"
      }
    }
  ]
}
JSON
}
if [ "$DRY_RUN" = "1" ]; then
    echo "  + write ${GO_SPEC} (pm2 spec: script=${INSTALL_DIR}/mirakurun-go, interpreter=none)"
else
    write_go_spec
fi

# ---- 3. 差し替え -----------------------------------------------------------
log "step 4/5: replacing pm2 app '${APP_NAME}'"
# Node 版を停止する前に必ず graceful に落とす (DB の最終保存は終了処理で行われる)
run pm2 stop "$APP_NAME"
sleep 2
run pm2 delete "$APP_NAME"
run pm2 start "$GO_SPEC"

health_check() {
    # /api/version が返り、チャンネル・サービスが 1 件以上ロードされていることを確認する
    local waited=0
    while [ "$waited" -lt "$HEALTH_TIMEOUT" ]; do
        if curl -sf -m 5 "${BASE_URL}/api/version" >/dev/null 2>&1; then
            local channels services tuners
            channels="$(curl -sf -m 5 "${BASE_URL}/api/channels" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))' 2>/dev/null || echo 0)"
            services="$(curl -sf -m 5 "${BASE_URL}/api/services" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))' 2>/dev/null || echo 0)"
            tuners="$(curl -sf -m 5 "${BASE_URL}/api/tuners"   | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))' 2>/dev/null || echo 0)"
            log "  health: channels=${channels} services=${services} tuners=${tuners} (waited ${waited}s)"
            if [ "${channels}" -gt 0 ] && [ "${services}" -gt 0 ] && [ "${tuners}" -gt 0 ]; then
                return 0
            fi
        fi
        sleep 2
        waited=$((waited + 2))
    done
    return 1
}

if [ "$DRY_RUN" = "1" ]; then
    log "step 5/5: health check (skipped in dry-run)"
    log "dry-run completed"
    exit 0
fi

log "step 5/5: health check"
if health_check; then
    run pm2 save
    log "SUCCESS: '${APP_NAME}' is now running the Go implementation."
    log "  backup: ${BACKUP_DIR}"
    log "  rollback: sudo $(dirname "$(readlink -f "$0")")/rollback.sh --backup-dir ${BACKUP_DIR}"
    exit 0
fi

# ---- 失敗時: 自動ロールバック ----------------------------------------------
warn "health check failed - rolling back to the Node implementation"
"${SCRIPT_DIR}/rollback.sh" --backup-dir "$BACKUP_DIR" --app-name "$APP_NAME" --base-url "$BASE_URL" || {
    die "rollback also failed. Restore manually with: pm2 start ${BACKUP_DIR}/app-spec.json && pm2 save"
}
die "cutover aborted and rolled back to Node (see pm2 logs)"
