/*
   Copyright 2016 kanreisa
   Copyright 2026 aki0429 (Go port)

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package core

import (
	"context"
	"os"
	"runtime"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/db"
	"github.com/aki0429/Mirakurun/go/internal/event"
	"github.com/aki0429/Mirakurun/go/internal/job"
	"github.com/aki0429/Mirakurun/go/internal/mlog"
)

// TunerAPI は tuner パッケージ (wave 2) が core に提供するインタフェース。
// core 側からチューナーへアクセスする唯一の経路。
type TunerAPI interface {
	// TypeExists は指定のチャンネル種別を扱えるチューナーが存在するか返す。
	TypeExists(apid.ChannelType) bool
	// ReadyForJob はチャンネルがジョブ実行可能 (チューナーが空き) か返す。
	ReadyForJob(ctx context.Context, item *ChannelItem) bool
	// GetServices はチャンネルからサービス一覧を取得する。
	GetServices(ctx context.Context, item *ChannelItem) ([]apid.Service, error)
	// GetEPG はチャンネルの EPG を duration だけ回収する (0 は既定時間)。
	GetEPG(ctx context.Context, item *ChannelItem, duration time.Duration) error
}

// Config は Node 版 `_.config` + `_.configIntegrity` + DB パスの移植。
type Config struct {
	Server            *apid.ConfigServer
	Channels          []apid.ConfigChannelsItem
	Tuners            []apid.ConfigTunersItem
	ChannelsIntegrity string

	ServicesDBPath  string
	ProgramsDBPath  string
	LogoDataDirPath string
}

// NewConfig は環境変数から DB パス類を解決した Config を作る。
func NewConfig() *Config {
	return &Config{
		ServicesDBPath:  envOr("SERVICES_DB_PATH", db.ServicesPath),
		ProgramsDBPath:  envOr("PROGRAMS_DB_PATH", db.ProgramsPath),
		LogoDataDirPath: envOr("LOGO_DATA_DIR_PATH", db.LogoDataDirPath),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// App は Node 版のグローバル `_` に相当する。
type App struct {
	Config *Config
	Log    *mlog.Logger
	Event  *event.Emitter
	Job    *job.Manager
	Clock  job.Clock

	Tuner   TunerAPI
	Channel *ChannelStore
	Service *ServiceStore
	Program *ProgramStore
	Status  *Status

	// Version は /api/status, /api/version が返すバージョン (package.json 相当)。
	Version string
}

// AppOptions は App の生成オプション。
type AppOptions struct {
	Config  *Config
	Tuner   TunerAPI
	Clock   job.Clock
	Log     *mlog.Logger
	Event   *event.Emitter
	Version string
	// MaxRunning / MaxStandby は job.Manager の同時実行数。0 なら既定値。
	MaxRunning int
	MaxStandby int
}

// DefaultVersion は Mirakurun 本体のバージョン (package.json)。
const DefaultVersion = "4.1.5"

// New は App を構築し、Channel / Service / Program / Status を初期化する。
// ストアの初期化でジョブ (EPG Gatherer, Program GC, Service 系) が登録される。
func New(opts AppOptions) *App {
	clock := opts.Clock
	if clock == nil {
		clock = job.RealClock{}
	}
	log := opts.Log
	if log == nil {
		log = mlog.Default()
	}
	ev := opts.Event
	if ev == nil {
		ev = event.New(event.Options{Now: func() time.Time { return clock.Now() }})
	}
	version := opts.Version
	if version == "" {
		version = DefaultVersion
	}

	a := &App{
		Config:  opts.Config,
		Log:     log,
		Event:   ev,
		Clock:   clock,
		Tuner:   opts.Tuner,
		Version: version,
	}
	if a.Config == nil {
		a.Config = NewConfig()
	}

	a.Job = job.New(job.Options{
		MaxRunning: opts.MaxRunning,
		MaxStandby: opts.MaxStandby,
		Clock:      clock,
		Event:      ev,
		Logger:     log,
	})

	a.Status = NewStatus(clock, log)
	a.Status.StartTimer()

	// ストア生成 (この時点でジョブ登録が走る)。
	a.Channel = newChannelStore(a)
	a.Service = newServiceStore(a)
	a.Program = newProgramStore(a)

	return a
}

// Load は Node 版 server.ts の起動順に倣って services.json → programs.json を読み込む。
func (a *App) Load() error {
	if err := a.Service.Load(); err != nil {
		return err
	}
	if err := a.Program.Load(); err != nil {
		return err
	}
	return nil
}

// GetStatus は /api/status の JSON を組み立てる (Node 版 api/status.ts getStatus)。
func (a *App) GetStatus() apid.Status {
	var st apid.Status
	st.Time = a.Clock.Now().UnixMilli()
	st.Version = a.Version

	st.Process.Arch = nodeArch(runtime.GOARCH)
	st.Process.Platform = nodePlatform(runtime.GOOS)
	st.Process.Versions = map[string]any{"go": runtime.Version()}
	st.Process.Env = processEnv()
	st.Process.PID = os.Getpid()
	st.Process.MemoryUsage = memoryUsage()

	st.EPG.GatheringNetworks = a.Status.GatheringNetworks()
	st.EPG.StoredEvents = a.Program.Len()

	st.RPCCount = int(a.Status.RPCCount())
	tunerDevices := 0
	if a.Status.TunerDeviceCount != nil {
		tunerDevices = a.Status.TunerDeviceCount()
	}
	st.StreamCount.TunerDevice = tunerDevices
	st.StreamCount.TSFilter = int(a.Status.TSFilterCount())
	st.StreamCount.Decoder = int(a.Status.DecoderCount())

	st.ErrorCount.UncaughtException = int(a.Status.ErrorCount(ErrorUncaughtException))
	st.ErrorCount.UnhandledRejection = int(a.Status.ErrorCount(ErrorUnhandledRejection))
	st.ErrorCount.BufferOverflow = int(a.Status.ErrorCount(ErrorBufferOverflow))
	st.ErrorCount.TunerDeviceRespawn = int(a.Status.ErrorCount(ErrorTunerDeviceRespawn))
	st.ErrorCount.DecoderRespawn = int(a.Status.ErrorCount(ErrorDecoderRespawn))

	last, m1, m5, m15 := a.Status.TimerAccuracy()
	st.TimerAccuracy.Last = float64(last) / 1000.0
	st.TimerAccuracy.M1 = minMaxAvgForTest(m1)
	st.TimerAccuracy.M5 = minMaxAvgForTest(m5)
	st.TimerAccuracy.M15 = minMaxAvgForTest(m15)

	return st
}

// nodeArch は Go の GOARCH を Node の process.arch 表記へ寄せる。
func nodeArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x64"
	case "386":
		return "ia32"
	case "arm64":
		return "arm64"
	default:
		return goarch
	}
}

// nodePlatform は Go の GOOS を Node の process.platform 表記へ寄せる。
func nodePlatform(goos string) string {
	switch goos {
	case "windows":
		return "win32"
	case "darwin":
		return "darwin"
	default:
		return goos
	}
}

// processEnv は Node 版 status が返す env サブセットを再現する (存在するキーのみ)。
func processEnv() map[string]any {
	keys := []string{
		"PATH", "DOCKER", "DOCKER_NETWORK", "pm_uptime", "NODE_ENV",
		"SERVER_CONFIG_PATH", "TUNERS_CONFIG_PATH", "CHANNELS_CONFIG_PATH",
		"SERVICES_DB_PATH", "PROGRAMS_DB_PATH", "LOGO_DATA_DIR_PATH",
	}
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			out[k] = v
		}
	}
	return out
}

// memoryUsage は Node 版 process.memoryUsage() の形へ Go の MemStats を寄せる。
func memoryUsage() map[string]any {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return map[string]any{
		"rss":          int64(m.Sys),
		"heapTotal":    int64(m.HeapSys),
		"heapUsed":     int64(m.HeapAlloc),
		"external":     int64(m.StackInuse),
		"arrayBuffers": int64(0),
	}
}
