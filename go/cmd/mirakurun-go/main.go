/*
KonomiTV: Mirakurun Go 版エントリーポイント

Node 版 src/server.ts と同じ起動順序で、
設定ロード → App/チューナー/サーバー生成 → DB ロード → サーバー起動 を行う。

- 環境変数の既定値 (SERVER_CONFIG_PATH など) は Node 版 server.ts の setEnv() と同じ。
- SETUP=true のときは DB をロードした直後に終了する (Node 版と同じ)。
- SIGINT / SIGTERM でサーバーとチューナーを停止する。
*/
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/config"
	"github.com/aki0429/Mirakurun/go/internal/core"
	"github.com/aki0429/Mirakurun/go/internal/mlog"
	"github.com/aki0429/Mirakurun/go/internal/server"
	"github.com/aki0429/Mirakurun/go/internal/tuner"
)

// setEnv は Node 版 server.ts の setEnv() と同じく、未設定のときだけ既定値を入れる。
func setEnv(name string, value string) {
	if os.Getenv(name) == "" {
		_ = os.Setenv(name, value)
	}
}

// tuneProcessPriority は Node 版 server.ts と同じく、root 実行時に renice / ionice を試みる。
// 失敗しても起動は継続する (Linux 以外やコマンド不在の環境で警告のみ)。
func tuneProcessPriority(log *mlog.Logger) {
	if runtime.GOOS != "linux" {
		log.Warn("running in not linux!")
	}
	if os.Geteuid() != 0 {
		log.Warn("running in not root!")
		return
	}

	// renice / ionice は失敗しても起動を止めない (Node 版も try/catch で警告のみ)。
	if err := exec.Command("renice", "-n", "-10", "-p", fmt.Sprintf("%d", os.Getpid())).Run(); err != nil {
		log.Warn("error on modify nice: %s", err)
	}
	if err := exec.Command("ionice", "-c", "1", "-n", "7", "-p", fmt.Sprintf("%d", os.Getpid())).Run(); err != nil {
		log.Warn("error on modify nice: %s", err)
	}
}

// tunerProxy は core.TunerAPI を実装し、生成順序の循環 (core.New がチューナーを必要とし、
// tuner.New が core.App を必要とする) を解く。
//
// channelStore のロードは core.New の中で走り、その時点で TunerAPI が非 nil でないと
// 全てのチャンネルが読み飛ばされる。そこで New 時点では設定 (tuners.yml) から判定した
// チャンネル種別で答え、実チューナーが生成された後はそれを委譲先にする。
type tunerProxy struct {
	types  map[apid.ChannelType]bool
	target *tuner.Tuner
}

// TypeExists は設定に該当種別のチューナーがあるかを返す (Node 版 Tuner#typeExists 相当)。
func (p *tunerProxy) TypeExists(typ apid.ChannelType) bool {
	if p.target != nil {
		return p.target.TypeExists(typ)
	}
	return p.types[typ]
}

// ReadyForJob はジョブ実行前にチューナーが空くのを待つ (Node 版 Tuner#readyForJob 相当)。
func (p *tunerProxy) ReadyForJob(ctx context.Context, item *core.ChannelItem) bool {
	if p.target == nil {
		return false
	}
	return p.target.ReadyForJob(ctx, item)
}

// GetServices はチャンネルのサービス一覧を取得する (サービススキャン)。
func (p *tunerProxy) GetServices(ctx context.Context, item *core.ChannelItem) ([]apid.Service, error) {
	if p.target == nil {
		return nil, fmt.Errorf("tuner is not ready")
	}
	return p.target.GetServices(ctx, item)
}

// GetEPG はチャンネルの EPG を取得する (EIT 収集)。
func (p *tunerProxy) GetEPG(ctx context.Context, item *core.ChannelItem, d time.Duration) error {
	if p.target == nil {
		return fmt.Errorf("tuner is not ready")
	}
	return p.target.GetEPG(ctx, item, d)
}

// tunerAdapter は *tuner.Tuner を server.Tuner インタフェースに適合させる。
//
// internal/server は tuner パッケージに依存しないよう独自の Tuner / UserRequest 型を
// 定義しているため、ユーザー要求の型変換だけをここで行う。
type tunerAdapter struct {
	tuner *tuner.Tuner
}

// Devices は全チューナーデバイスの状態を返す。
func (a *tunerAdapter) Devices() []apid.TunerDevice { return a.tuner.Devices() }

// Get は index 一致のデバイスを返す。存在しない場合は nil を返す
// (nil ポインタをインタフェースとして返さないよう明示的に判定する)。
func (a *tunerAdapter) Get(index int) server.TunerDevice {
	device := a.tuner.Get(index)
	if device == nil {
		return nil
	}
	return device
}

// DeviceCount はチューナーデバイス数を返す。
func (a *tunerAdapter) DeviceCount() int { return a.tuner.DeviceCount() }

// toTunerUserRequest は server 側のユーザー要求を tuner 側の同名構造体に変換する。
func toTunerUserRequest(u server.UserRequest) tuner.UserRequest {
	return tuner.UserRequest{
		ID:             u.ID,
		Priority:       u.Priority,
		Agent:          u.Agent,
		URL:            u.URL,
		DisableDecoder: u.DisableDecoder,
	}
}

// InitChannelStream はチャンネル全体の TS ストリームを開始する。
func (a *tunerAdapter) InitChannelStream(
	ch *core.ChannelItem,
	userReq server.UserRequest,
	output io.Writer,
) (server.TSFilter, error) {
	filter, err := a.tuner.InitChannelStream(ch, toTunerUserRequest(userReq), output)
	if err != nil {
		return nil, err
	}
	if filter == nil {
		return nil, nil
	}
	return filter, nil
}

// InitServiceStream はサービス単位の TS ストリームを開始する。
func (a *tunerAdapter) InitServiceStream(
	svc *core.ServiceItem,
	userReq server.UserRequest,
	output io.Writer,
) (server.TSFilter, error) {
	filter, err := a.tuner.InitServiceStream(svc, toTunerUserRequest(userReq), output)
	if err != nil {
		return nil, err
	}
	if filter == nil {
		return nil, nil
	}
	return filter, nil
}

// InitProgramStream は番組単位の TS ストリームを開始する。
func (a *tunerAdapter) InitProgramStream(
	prog *apid.Program,
	userReq server.UserRequest,
	output io.Writer,
) (server.TSFilter, error) {
	filter, err := a.tuner.InitProgramStream(prog, toTunerUserRequest(userReq), output)
	if err != nil {
		return nil, err
	}
	if filter == nil {
		return nil, nil
	}
	return filter, nil
}

// typesFromTuners は tuners.yml の有効なチューナーが扱える種別を集める。
func typesFromTuners(tuners []apid.ConfigTunersItem) map[apid.ChannelType]bool {
	types := map[apid.ChannelType]bool{}
	for _, item := range tuners {
		// 無効化されたチューナーは Node 版と同じく対象外。
		if item.IsDisabled {
			continue
		}
		for _, typ := range item.Types {
			types[typ] = true
		}
	}
	return types
}

func main() {
	// Node 版 server.ts と同じ既定パスを、未設定のときだけ入れる。
	setEnv("SERVER_CONFIG_PATH", "/usr/local/etc/mirakurun/server.yml")
	setEnv("TUNERS_CONFIG_PATH", "/usr/local/etc/mirakurun/tuners.yml")
	setEnv("CHANNELS_CONFIG_PATH", "/usr/local/etc/mirakurun/channels.yml")
	setEnv("SERVICES_DB_PATH", "/usr/local/var/db/mirakurun/services.json")
	setEnv("PROGRAMS_DB_PATH", "/usr/local/var/db/mirakurun/programs.json")
	setEnv("LOGO_DATA_DIR_PATH", "/usr/local/var/db/mirakurun/logo-data")

	logger := mlog.Default()
	tuneProcessPriority(logger)

	loader := config.NewLoader()

	// 設定ファイルのロード (server.yml → channels.yml → tuners.yml の順)。
	serverConfig, err := loader.LoadServer()
	if err != nil {
		logger.Fatal("failed to load server config: %s", err)
		os.Exit(1)
	}
	channels, channelsIntegrity, err := loader.LoadChannelsWithIntegrity()
	if err != nil {
		logger.Fatal("failed to load channels config: %s", err)
		os.Exit(1)
	}
	tuners, err := loader.LoadTuners()
	if err != nil {
		logger.Fatal("failed to load tuners config: %s", err)
		os.Exit(1)
	}

	// ログ設定は設定ファイルの値で上書きできる (Node 版 server.ts と同じ)。
	if serverConfig.LogLevel != nil {
		logger.SetLevel(mlog.Level(*serverConfig.LogLevel))
	}
	if serverConfig.MaxLogHistory != nil {
		logger.SetMaxHistory(*serverConfig.MaxLogHistory)
	}

	appConfig := core.NewConfig()
	appConfig.Server = serverConfig
	appConfig.Channels = channels
	appConfig.Tuners = tuners
	appConfig.ChannelsIntegrity = channelsIntegrity

	// チューナーと App は相互参照するため、先に proxy を渡して生成順序を解決する。
	proxy := &tunerProxy{types: typesFromTuners(tuners)}
	app := core.New(core.AppOptions{
		Config: appConfig,
		Tuner:  proxy,
		Log:    logger,
	})
	tunerInstance := tuner.New(app)
	proxy.target = tunerInstance

	// DB は services.json → programs.json の順に読む (Node 版 server.ts と同じ)。
	if err := app.Load(); err != nil {
		logger.Fatal("failed to load db: %s", err)
		os.Exit(1)
	}

	// SETUP=true は設定ファイルと DB の初期化のみ行って終了する。
	if os.Getenv("SETUP") == "true" {
		logger.Info("setup is done.")
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// チューナーデバイス (子プロセス) を起動する。
	if err := tunerInstance.Start(ctx); err != nil {
		logger.Fatal("failed to start tuner: %s", err)
		os.Exit(1)
	}

	serverInstance := server.New(server.Options{
		App:    app,
		Tuner:  &tunerAdapter{tuner: tunerInstance},
		Logger: logger,
	})
	if err := serverInstance.Init(ctx); err != nil {
		logger.Fatal("failed to init server: %s", err)
		os.Exit(1)
	}

	// シグナルを受けるまで待機する。
	<-ctx.Done()
	logger.Info("shutting down...")

	// 終了処理はサーバー → チューナーの順 (Node 版 Server#close 相当)。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := serverInstance.Shutdown(shutdownCtx); err != nil {
		logger.Warn("failed to shutdown server: %s", err)
	}
	tunerInstance.Shutdown()
}
