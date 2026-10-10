# Mirakurun Go 版 (`go/`)

Node.js 版 Mirakurun (本家 `Chinachu/Mirakurun` 4.1.5) のバックエンドを Go へ移植した実装です。
目的は**メモリ使用量の削減**で、本番実測で Node 版が RSS 約 500MB / 120MB (2 インスタンス) を占めていたものを、
静的リンクの単一バイナリ (約 11.7MB) に置き換えます。

外部クライアント (EDCB + BonDriver_mirakc、KonomiTV、Mirakurun Web UI、EPGStation、Miranda 等) から
**そのまま使えること**を目標に、以下を Node 版と互換に保っています。

- HTTP API のパス・レスポンス形式・エラー形式 (`{"code":...}`)・CORS / COEP / CORP ヘッダ
- 設定ファイル (`server.yml` / `tuners.yml` / `channels.yml`) の全フィールドと解釈
- DB (`services.json` / `programs.json`) のスキーマ・日時形式・`__integrity__` ハッシュ
- 環境変数 (`SERVER_CONFIG_PATH` など 6 変数) と既定値、`SETUP=true` の挙動
- ログの書式 (`2026-10-09T10:24:22.103+09:00 info: ...`) とログレベル
- Web UI (`lib/ui` の Node/webpack ビルド成果物) の静的配信、RPC (WebSocket) のエンドポイント

Web UI 自体の Go 化は対象外です (Node ビルド成果物をそのまま配信します)。

## パッケージ構成

| パッケージ | 役割 | 対応する Node 実装 |
|---|---|---|
| `internal/apid` | API の型定義・チャンネル ID 変換・順序保持 JSON | `src/Mirakurun/api.ts`, `ChannelItem.ts`, `ServiceItem.ts` |
| `internal/aribts` | MPEG2-TS 解析 (PSI/SI, EIT/TOT/CDT, DSM-CC, ロゴ, ARIB 文字) | `@chinachu/aribts` |
| `internal/config` | 設定ロード・バリデーション・整合性ハッシュ | `src/Mirakurun/config.ts`, `src/Mirakurun/Channel.ts` |
| `internal/db` | JSON DB の読み書き (遅延保存・整合性ヘッダ) | `src/Mirakurun/db.ts` |
| `internal/core` | Channel / Service / Program ストアとステータス・イベント | `src/Mirakurun/{Channel,Service,Program,Status,Event}.ts` |
| `internal/job` | ジョブとスケジュール (`* * * * *` 形式) | `src/Mirakurun/Job.ts` |
| `internal/tuner` | TunerDevice 管理・チューナー選択・TSFilter・TSDecoder・EPG | `src/Mirakurun/{Tuner,TunerDevice,TSFilter,TSDecoder,EPG}.ts` |
| `internal/server` | HTTP ルーティング・アクセス制御・unix socket・RPC・UI 配信 | `src/Mirakurun/Server.ts`, `src/Mirakurun/api/*.ts`, `rpc.ts` |
| `internal/mlog` | ロガー (ログレベル・履歴保持) | ログ基盤 |
| `internal/event` | イベントバス | `events` |
| `cmd/mirakurun-go` | エントリーポイント (起動順序・シグナル処理) | `src/server.ts` |

## ビルド

```sh
cd go

# Linux (本番向け。CGO 不要のためクロスコンパイル可)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o mirakurun-go-linux-amd64 ./cmd/mirakurun-go

# 開発マシン上 (Windows / macOS)
go build -o mirakurun-go.exe ./cmd/mirakurun-go
```

外部依存は `gopkg.in/yaml.v3` と `github.com/coder/websocket` のみです。

## 起動

Node 版と同じ環境変数・既定値を使います。実行時に設定しなければ既定値が入ります。

| 環境変数 | 既定値 |
|---|---|
| `SERVER_CONFIG_PATH` | `/usr/local/etc/mirakurun/server.yml` |
| `TUNERS_CONFIG_PATH` | `/usr/local/etc/mirakurun/tuners.yml` |
| `CHANNELS_CONFIG_PATH` | `/usr/local/etc/mirakurun/channels.yml` |
| `SERVICES_DB_PATH` | `/usr/local/var/db/mirakurun/services.json` |
| `PROGRAMS_DB_PATH` | `/usr/local/var/db/mirakurun/programs.json` |
| `LOGO_DATA_DIR_PATH` | `/usr/local/var/db/mirakurun/logo-data` |

```sh
# 通常起動 (root で実行すると renice -10 / ionice を試行する。失敗しても起動は継続)
./mirakurun-go

# 設定と DB の読み込みだけを行って終了する (Node 版の SETUP=true と同じ)
SETUP=true ./mirakurun-go
```

`-q1ud` のような 2 つ目のインスタンスは、上記 3 つの設定パスを切り替えて起動します
(本番では root の pm2 に 2 エントリ登録されています)。

## 検証結果

移植の正しさは、Node 版との**差分テスト (differential oracle)** で確認しています。

### 静的・単体検証

| 項目 | 結果 |
|---|---|
| `gofmt -l .` / `go build ./...` / `go vet ./...` | 全 10 パッケージ クリーン |
| `go test ./...` | 全 10 パッケージ `ok` |

### 本番データを使った差分検証

| 対象 | 方法 | 結果 |
|---|---|---|
| チャンネル整合性ハッシュ | 本番 `channels.yml` から再計算し、本番 `services.json` の `__integrity__` 記録値と比較 | 3 種すべて一致 |
| ARIB TS パーサ | 本番録画の生 TS (GR 64MB / BS 75MB) を Node `@chinachu/aribts` と 1 セクションずつ比較 | 12,222 / 12,222 一致、文字列不一致 0 |
| EPG 取り込み | BS 生 TS から生成した番組情報を本番 `programs.json` と照合 | 3,463 / 3,463 一致、conflict 0 |
| API レスポンス | 本番設定・DB をコピーして起動し、Node 版と全エンドポイントを突合 | channels 47/47、services 141/141、tuners 8/8、iptv lineup 14,593 バイト完全一致、jobs の項目スキーマ一致 |

### 意図的な差分 (Node 版と挙動が異なる点)

いずれも実クライアントが消費しない、あるいは仕様上問題にならない差分です。

| 差分 | 内容 |
|---|---|
| `commandVars` | `/api/channels` のレスポンスに含めていません。KonomiTV・Mirakurun Web UI のいずれも参照していないことを確認済みです |
| 設定値の `isDisabled` | `false` のときフィールドを省略します (Node は `false` を明示)。値を `true` に戻す PUT は受け付けます |
| ヘッダ名の大小 | `X-Mirakurun-Tuner-User-Id` のように Go の正規化が入ります。HTTP 仕様上ヘッダ名はケース非依存のため影響ありません |
| 404 / 400 の使い分け | 存在しない ID は Go は `404`、Node は OpenAPI の検証で `400` を返します |

## 既知の残件

- **実チューナーからのストリーム配信は未検証です。** 移植の検証はチューナーコマンドを無害な no-op (`/bin/cat /dev/null`) に置き換えて行っており、実際のチューナーから TS を読み出して配信する経路 (CC ドロップ 0 の実測を含む) は本番切替時に確認が必要です。
- **実波のロゴカルーセルは未検証です。** 手元の録画 2 本にロゴのモジュール (`LOGO-05`) が含まれておらず、Node 版も同理由でロゴを保存しないことを確認したため、合成データでのバイト一致検証に留めています。
- `-q1ud` 側インスタンスでの実機比較は未実施です (設定読み込み経路は同一)。

## 本番環境への導入

本番 (root の pm2 で 2 インスタンスを運用) への切り替えは `deploy/` の手順に従って行います。
切替は**設定・DB・pm2 状態をバックアップし、ヘルスチェックに失敗したら自動で元に戻す**スクリプトとして用意してあり、
実行者 (root 権限を持つ管理者) が明示的に実行するまで本番は変更されません。

- `deploy/README.md` — 切替手順と前提条件
- `deploy/cutover.sh` — 事前確認 → バックアップ → 差し替え → ヘルスチェック → 失敗時の自動ロールバック
- `deploy/rollback.sh` — バックアップから Node 版へ戻す

## ブランチ運用

Go 実装は **`feature/go-rewrite` ブランチで管理**します。`master` は本家 `Chinachu/Mirakurun` の追従専用とし、
Go の変更を入れません (本家の更新を素直に取り込める状態を保つため)。

| ブランチ | 役割 | 入れてよい変更 |
|---|---|---|
| `master` | 本家 `Chinachu/Mirakurun` の同期用 | upstream の取り込みのみ |
| `feature/go-rewrite` | Go 実装の管理ブランチ | `go/` 配下のみ (Node 実装 `src/` は変更しない) |

### 本家の更新を取り込む

```sh
git fetch upstream
git checkout master && git merge --ff-only upstream/master && git push origin master
git checkout feature/go-rewrite && git merge master      # 競合したら go/ 側で解決する
git push origin feature/go-rewrite
```

### 変更を入れるときの確認

```sh
cd go
gofmt -l . && go vet ./... && go test ./...        # 静的解析と単体テスト
../go/deploy/parity-check.sh --config-dir /usr/local/etc/mirakurun --port 40776 \
    --node-url http://127.0.0.1:40772              # 本番の設定・DB を扱う変更は Node 版と差分確認
```

本番の切り替えは `deploy/cutover.sh` を管理者 (root) が実行します。切り替えは常に独立したステップであり、
このブランチを更新しても本番は変更されません。
