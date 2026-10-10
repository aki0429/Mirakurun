#!/usr/bin/env bash
#
# 本番 Node 版と Go 版を同じホスト上で並行起動し、API レスポンスを突合するスクリプト
#
# 本番 (40772 等) はそのまま動かしたまま、別ポートで Go 版を起動して比較する。
# チューナーコマンドは無害な no-op (/bin/cat /dev/null) に置き換えるため、
# 実際のチューナーには一切アクセスしない (録画や視聴を妨げない)。
# DB は本番からコピーしたものを読み書きする。
#
# 前提: root 権限は不要。本番の設定・DB を読める権限があればよい。
#
# 使い方:
#   ./parity-check.sh                        # 既定: 本番 40772 / Go 40776
#   ./parity-check.sh --node-url http://127.0.0.1:40772 --port 40776
#   ./parity-check.sh --config-dir /usr/local/etc/mirakurun-q1ud --port 40778
#
set -euo pipefail

CONFIG_DIR="${CONFIG_DIR:-/usr/local/etc/mirakurun}"
NODE_URL="${NODE_URL:-http://127.0.0.1:40772}"
GO_PORT="${GO_PORT:-40776}"
WORK_DIR="${WORK_DIR:-$HOME/mirakurun-go-test}"
BINARY="${BINARY:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/mirakurun-go-linux-amd64}"
NOOP_COMMAND="${NOOP_COMMAND:-/bin/cat /dev/null}"
# 本番 DB の置き場所。2 つ目のインスタンスは専用のディレクトリを使う (例: /usr/local/var/db/mirakurun-q1ud)
DB_DIR="${DB_DIR:-/usr/local/var/db/mirakurun}"

while [ $# -gt 0 ]; do
    case "$1" in
        --config-dir) CONFIG_DIR="$2"; shift 2 ;;
        --node-url)   NODE_URL="$2"; shift 2 ;;
        --port)       GO_PORT="$2"; shift 2 ;;
        --work-dir)   WORK_DIR="$2"; shift 2 ;;
        --binary)     BINARY="$2"; shift 2 ;;
        --db-dir)     DB_DIR="$2"; shift 2 ;;
        -h|--help)    grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "unknown option: $1" >&2; exit 1 ;;
    esac
done

log() { echo "[parity] $*"; }
die() { echo "[parity] ERROR: $*" >&2; exit 1; }

[ -f "$BINARY" ] || die "binary not found: ${BINARY}"
# scp でコピーすると実行ビットが落ちることがあるため、ここで補う
if [ ! -x "$BINARY" ]; then
    chmod +x "$BINARY" 2>/dev/null || true
fi
[ -x "$BINARY" ] || die "binary is not executable: ${BINARY} (chmod +x it after copying)"
[ -f "${CONFIG_DIR}/server.yml" ] || die "config not found: ${CONFIG_DIR}/server.yml"

GO_URL="http://127.0.0.1:${GO_PORT}"
SOCKET="${WORK_DIR}/mirakurun-go.sock"

log "config=${CONFIG_DIR} node=${NODE_URL} go=${GO_URL} work=${WORK_DIR}"
mkdir -p "$WORK_DIR/db" "$WORK_DIR/logos"

# ---- 1. 本番の設定をコピーし、ポートとソケットだけ差し替える ----------------
python3 - "$CONFIG_DIR/server.yml" "$WORK_DIR/server.yml" "$GO_PORT" "$SOCKET" <<'PY'
import re, sys
src, dst, port, sock = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
text = open(src, encoding='utf-8').read()
text = re.sub(r'(?m)^(\s*port:\s*)\S+', r'\g<1>' + port, text, count=1)
text = re.sub(r'(?m)^(\s*path:\s*)\S+', r'\g<1>' + sock, text, count=1)
open(dst, 'w', encoding='utf-8').write(text)
print(f'  server.yml: port={port} socket={sock}')
PY

# ---- 2. チューナーコマンドを no-op に差し替える (ハードウェア非接触) ---------
python3 - "$CONFIG_DIR/tuners.yml" "$WORK_DIR/tuners.yml" "$NOOP_COMMAND" <<'PY'
import sys
import yaml
src, dst, noop = sys.argv[1], sys.argv[2], sys.argv[3]
data = yaml.safe_load(open(src, encoding='utf-8'))

# tuners.yml は「チューナーの配列」そのもの。将来 dict 形式 (tuners: [...]) にも
# 対応できるよう、両方の形を受け付ける。
if isinstance(data, list):
    items = data
elif isinstance(data, dict) and isinstance(data.get('tuners'), list):
    items = data['tuners']
else:
    raise SystemExit(f'unsupported tuners.yml structure: {type(data).__name__}')

count = 0
for t in items:
    if isinstance(t, dict) and t.get('command'):
        t['command'] = noop
        count += 1
yaml.safe_dump(data, open(dst, 'w', encoding='utf-8'), allow_unicode=True, sort_keys=False)
print(f'  tuners.yml: replaced {count} command(s) with {noop}')
PY
cp -a "${CONFIG_DIR}/channels.yml" "$WORK_DIR/channels.yml"

# ---- 3. DB を本番からコピーする (読み書きはコピーに対して行う) --------------
if [ -r "${DB_DIR}/services.json" ]; then
    cp -a "${DB_DIR}/services.json" "$WORK_DIR/db/services.json"
    cp -a "${DB_DIR}/programs.json" "$WORK_DIR/db/programs.json"
    [ -d "${DB_DIR}/logo-data" ] && cp -a "${DB_DIR}/logo-data/." "$WORK_DIR/logos/" 2>/dev/null
    log "copied DB from ${DB_DIR} ($(stat -c %s "$WORK_DIR/db/programs.json") bytes programs)"
else
    : > "$WORK_DIR/db/services.json"; : > "$WORK_DIR/db/programs.json"
    log "production DB is not readable; starting with an empty DB"
fi

# ---- 4. Go 版を別ポートで起動する ------------------------------------------
# 既に同じポートで動いているプロセスがあれば中断する (二重起動の防止)
if curl -sf -m 3 "${GO_URL}/api/version" >/dev/null 2>&1; then
    die "${GO_URL} is already in use (stop the previous instance first)"
fi

log "starting the Go implementation on ${GO_URL}"
SERVER_CONFIG_PATH="$WORK_DIR/server.yml" \
TUNERS_CONFIG_PATH="$WORK_DIR/tuners.yml" \
CHANNELS_CONFIG_PATH="$WORK_DIR/channels.yml" \
SERVICES_DB_PATH="$WORK_DIR/db/services.json" \
PROGRAMS_DB_PATH="$WORK_DIR/db/programs.json" \
LOGO_DATA_DIR_PATH="$WORK_DIR/logos" \
nohup "$BINARY" > "$WORK_DIR/go.log" 2>&1 &
GO_PID=$!
echo "$GO_PID" > "$WORK_DIR/mirakurun.pid"
trap 'kill '"$GO_PID"' 2>/dev/null || true' EXIT

for i in $(seq 1 30); do
    curl -sf -m 3 "${GO_URL}/api/version" >/dev/null 2>&1 && break
    sleep 1
done
curl -sf -m 3 "${GO_URL}/api/version" >/dev/null 2>&1 || { tail -30 "$WORK_DIR/go.log"; die "the Go implementation did not start"; }
log "started (pid ${GO_PID}). waiting 20s for the initial loads to settle..."
sleep 20

# ---- 5. 両インスタンスの主要エンドポイントを突合する ------------------------
# 差分があってもここでは停止せず、結果を集計して最後に終了コードで返す。
# 「一致すべきもの」だけを FAIL とし、version / 稼働時間 / DB 鮮度に依存するものは INFO として表示する。
set +e
python3 - "$NODE_URL" "$GO_URL" <<'PY'
import json, sys, urllib.request

node, go = sys.argv[1], sys.argv[2]

def get(base, path):
    with urllib.request.urlopen(base + path, timeout=20) as r:
        raw = r.read()
    try:
        return json.loads(raw)
    except Exception:
        return raw.decode('utf-8', 'replace')

def fetch(base, path):
    try:
        return get(base, path), None
    except Exception as e:
        return None, str(e)

print(f"{'endpoint':<32}{'node':>9}{'go':>9}  result")

# 1) 一致しなければならないもの: 実装差ではなくデータ差が出てはいけないエンドポイント
#    channels は id を持たないため (type, channel) の組で同一性を判定する。
hard_fail = 0
for path, keyf, label in [
    ('/api/channels', lambda x: (x.get('type'), x.get('channel')), 'type+channel'),
    ('/api/services', lambda x: x.get('id'), 'id'),
]:
    n, ne = fetch(node, path)
    g, ge = fetch(go, path)
    if ne or ge:
        print(f"{path:<32}{'':>18}  FETCH-ERROR node={ne} go={ge}")
        hard_fail += 1
        continue
    ns, gs = {keyf(x) for x in n}, {keyf(x) for x in g}
    equal = ns == gs
    print(f"{path:<32}{len(n):>9}{len(g):>9}  {label} equal={equal} only_node={len(ns - gs)} only_go={len(gs - ns)}")
    if not equal:
        hard_fail += 1
        for x in sorted(ns - gs, key=str)[:3]:
            print(f"{'':<32}  only in node: {x}")
        for x in sorted(gs - ns, key=str)[:3]:
            print(f"{'':<32}  only in go  : {x}")

# tuners: index と types の集合が一致すること
n, ne = fetch(node, '/api/tuners')
g, ge = fetch(go, '/api/tuners')
if ne or ge:
    print(f"{'/api/tuners':<32}{'':>18}  FETCH-ERROR node={ne} go={ge}")
    hard_fail += 1
else:
    idx_n = {t['index']: tuple(t.get('types') or []) for t in n}
    idx_g = {t['index']: tuple(t.get('types') or []) for t in g}
    equal = idx_n == idx_g
    print(f"{'/api/tuners':<32}{len(n):>9}{len(g):>9}  index/types equal={equal}")
    if not equal:
        hard_fail += 1

# iptv lineup: チャンネル定義由来のため実装差が出てはいけない。
# ただし URL にはインスタンス自身の base URL (ポート) が入るため、自分の base URL を正規化して比較する。
n, ne = fetch(node, '/api/iptv/lineup.json')
g, ge = fetch(go, '/api/iptv/lineup.json')
if ne or ge:
    print(f"{'/api/iptv/lineup.json':<32}{'':>18}  FETCH-ERROR node={ne} go={ge}")
    hard_fail += 1
else:
    def norm_urls(x, base):
        return json.loads(json.dumps(x, ensure_ascii=False).replace(base, 'BASE'))
    equal = norm_urls(n, node) == norm_urls(g, go)
    print(f"{'/api/iptv/lineup.json':<32}{len(n):>9}{len(g):>9}  content equal={equal} (URL のポートは正規化)")
    if not equal:
        hard_fail += 1
        for a, b in zip(n, g):
            if a != b:
                print(f"{'':<32}  node: {a}")
                print(f"{'':<32}  go  : {b}")
                break

# 2) 稼働状態に依存するもの (差分が出ても異常ではない。目視確認用に表示するだけ)
print()
print('--- 稼働状態に依存する値 (差分は異常とは限らない) ---')
for path in ['/api/version', '/api/tuners', '/api/jobs', '/api/job-schedules',
             '/api/services', '/api/iptv/lineup_status.json', '/api/iptv/discover.json']:
    n, ne = fetch(node, path)
    g, ge = fetch(go, path)
    if ne or ge:
        print(f"{path:<32}  node_err={ne} go_err={ge}")
        continue
    if isinstance(n, str):
        print(f"{path:<32}{n:>12}  vs {g:<12} {'same' if n == g else 'DIFF'}")
    elif isinstance(n, list):
        print(f"{path:<32}{len(n):>9}{len(g):>9}  {'same count' if len(n) == len(g) else 'count differs (uptime / live EPG)'}")
    elif isinstance(n, dict):
        only_n, only_g = set(n) - set(g), set(g) - set(n)
        print(f"{path:<32}  keys only_node={sorted(only_n)[:3]} only_go={sorted(only_g)[:3]}")

print()
if hard_fail == 0:
    print('RESULT: PASS (必須項目はすべて一致。上記の稼働状態依存の値は目視確認すること)')
else:
    print(f'RESULT: FAIL ({hard_fail} 項目が不一致)')
sys.exit(0 if hard_fail == 0 else 1)
PY
PARITY_RC=$?
set -e

# 成功した場合は確認できるようプロセスを残す (失敗時は trap が停止する)
if [ "$PARITY_RC" = "0" ]; then
    trap - EXIT
    log "done. the Go instance is still running on ${GO_URL} (pid $(cat "$WORK_DIR/mirakurun.pid"))"
    log "stop it with: kill \$(cat ${WORK_DIR}/mirakurun.pid)"
fi
exit "$PARITY_RC"
