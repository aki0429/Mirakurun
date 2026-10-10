# 本番環境への導入手順 (Node 版 → Go 版)

本番では Mirakurun が **root の pm2** で 2 インスタンス運用されています (`Mirakurun` と、`-q1ud` 用の別エントリ)。
本ディレクトリのスクリプトは、この構成を前提に「**バックアップ → 差し替え → ヘルスチェック → 失敗時は自動で元に戻す**」を
1 回の実行で行います。**管理者 (root) が明示的に実行するまで本番は変更されません。**

## 前提条件

- `pm2` が root のプロファイル (`/root/.pm2`) に入っていること。`sudo pm2 list` で対象アプリ名を確認できること。
- `curl` と `python3` (3.6 以降) が使えること。
- 本番の設定・DB のパスが Go 版の既定値と同じであること:
  - 設定: `/usr/local/etc/mirakurun{,-q1ud}/{server,tuners,channels}.yml`
  - DB: `/usr/local/var/db/mirakurun/{services,programs}.json`、ロゴ: `/usr/local/var/db/mirakurun/logo-data`
- 本番の `channels.yml` / DB は Go 版からも読み書きできる権限であること (DB は Node 版と同じファイルを共有します)。

## 手順

### 1. バイナリをビルドして本番ホストへ転送する

```sh
cd go
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o mirakurun-go-linux-amd64 ./cmd/mirakurun-go
sha256sum mirakurun-go-linux-amd64
scp mirakurun-go-linux-amd64 deploy/{cutover,rollback,parity-check}.sh <host>:~/mirakurun-go-deploy/
```

### 2. 並行比較で互換性を確認する (任意だが推奨)

本番を止めずに、別ポートで Go 版を起動して Node 版と API を突合します。
チューナーコマンドは `/bin/cat /dev/null` に置き換えるため**ハードウェアには触れません**。

```sh
cd ~/mirakurun-go-deploy
chmod +x *.sh
./parity-check.sh --config-dir /usr/local/etc/mirakurun     --port 40776 --node-url http://127.0.0.1:40772
./parity-check.sh --config-dir /usr/local/etc/mirakurun-q1ud --port 40778 --node-url http://127.0.0.1:40773
kill $(cat ~/mirakurun-go-test/mirakurun.pid)   # 確認が終わったら停止する
```

`RESULT: PASS` が出ることを確認してください。`version` (リポジトリ版と稼働版の差)、`jobs`/`job-schedules` (起動時間の差)、
`hasLogoData` (ロゴ実体の有無) は起動直後は差分として出ることがあるため、差分が出た場合は内容を確認してください。

### 3. 切替内容を確認する (dry-run)

```sh
sudo ./cutover.sh --app-name Mirakurun --dry-run
```

実行されるコマンドが表示されるだけで、何も変更されません。

### 4. 切替を実行する

```sh
# 本体インスタンス
sudo ./cutover.sh --app-name Mirakurun --base-url http://127.0.0.1:40772

# -q1ud インスタンス (名前は `sudo pm2 list` で確認する)
sudo ./cutover.sh --app-name Mirakurun-q1ud --base-url http://127.0.0.1:40773 \
    --config-dir /usr/local/etc/mirakurun-q1ud
```

スクリプトは次の順に処理します。

1. **事前確認** — 本番設定の**コピー**を使い、`SETUP=true` で Go バイナリが設定と DB を読めるか確認する (本番ファイルには書き込まない)
2. **バックアップ** — `/usr/local/var/backups/mirakurun-go/<日時>/` に、対象アプリの pm2 起動仕様 (`app-spec.json`)、
   `dump.pm2`、設定ファイル 3 点を保存する
3. **差し替え** — Node 版を graceful に停止 (終了処理で DB が保存される) し、pm2 エントリを Go バイナリに置き換える
4. **ヘルスチェック** — `/api/version`・`/api/channels`・`/api/services`・`/api/tuners` を最大 60 秒ポーリングし、
   チャンネル・サービス・チューナーがそれぞれ 1 件以上ロードされることを確認する
5. **失敗時** — `rollback.sh` を呼び出して Node 版に戻し、非 0 で終了する

### 5. 切替後に確認すること

スクリプトのヘルスチェックが通っても、**実チューナーからの配信経路は検証されていません**。
以下を実際に確認してください。

```sh
# ログ (DB 保存やチューナーの起動に失敗していないか)
sudo pm2 logs Mirakurun --lines 50

# 実際の視聴 (クライアントから 1 チャンネル開く) と、録画予約 → 録画ファイルの生成
# EDCB (BonDriver_mirakc) からのストリーム取得も併せて確認する
curl -sN --max-time 10 http://127.0.0.1:40772/api/channels/GR/T13/stream | wc -c
```

## 元に戻す (ロールバック)

```sh
sudo ./rollback.sh --list                                              # バックアップの一覧
sudo ./rollback.sh                                                     # 直近 (LATEST) から戻す
sudo ./rollback.sh --backup-dir /usr/local/var/backups/mirakurun-go/20261009-103000
sudo ./rollback.sh --app-name Mirakurun-q1ud --base-url http://127.0.0.1:40773 --backup-dir <...>
```

`app-spec.json` から pm2 エントリを復元します。応答が戻らない場合は `dump.pm2` から `pm2 kill` → `pm2 resurrect` で
pm2 の状態全体を巻き戻します。終了時は必ず `pm2 save` されるため、次回起動時も同じ状態で立ち上がります。

## 注意点

- **DB は Node 版と共有します。** Go 版は同じスキーマ・同じ日時形式 (`2026-10-09 10:25:33.123456+09:00`) で保存し、
  `__integrity__` ハッシュも同じ値を出力します。切替を一時的に戻しても DB はそのまま使えます。
- **切り戻し時に DB の再スキャンは不要です。** 両実装とも起動時に読み込むだけです。
- **実チューナーの配信は未検証です。** 切替は上記手順 5 の確認が終わるまで「完了」としないでください。
- 一度に 1 インスタンスずつ切り替えてください (2 インスタンス同時の切替は、切り分けができなくなります)。
