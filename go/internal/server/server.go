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

// Package server は Node 版 src/Mirakurun/Server.ts + api/** の HTTP 層を移植する。
//
// 設計方針:
//   - ルーティングは Go 1.22 以降の net/http.ServeMux (メソッド付きパターン)。
//     Node 版 express-openapi のルート定義 (src/Mirakurun/api/**) をそのまま写している。
//   - JSON ボディは Node 版 JSON.stringify / yieldable-json と同じく HTML エスケープなしで出力する
//     (apid.go の構造体フィールド順 = api.d.ts と同じ順)。
//   - エラーボディは api.ts の凍結形 {code, reason, errors} (reason は空なら null, errors は常に [])。
//   - アクセス制御 (IP/CIDR + Origin/Referer) と CORS は Server.ts のミドルウェア順を再現する。
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/config"
	"github.com/aki0429/Mirakurun/go/internal/core"
	"github.com/aki0429/Mirakurun/go/internal/mlog"
)

const (
	// DefaultPort は server.yml に port が無い場合に使う既定 TCP ポート (Node 版 config/server.yml)。
	DefaultPort = 40772

	// 既定の静的ファイル配置 (Node 版 Server.ts の express.static と同じ相対パス)。
	DefaultUIDir        = "lib/ui"
	DefaultUIIndex      = "lib/ui/index.html"
	DefaultRedocDir     = "node_modules/redoc/bundles"
	DefaultRedocTryDir  = "node_modules/redoc-try/dist"
	DefaultRedocUIHTML  = "lib/ui/redoc-ui.html"
	serverIdleTimeout   = 15 * time.Second
	rpcPingInterval     = 30 * time.Second
	defaultMaxLogBuffer = 512
)

// ConfigLoader は設定ファイルの読み書き (config.Loader が満たす)。
type ConfigLoader interface {
	LoadServer() (*apid.ConfigServer, error)
	SaveServer(cfg *apid.ConfigServer) error
	LoadTuners() ([]apid.ConfigTunersItem, error)
	SaveTuners([]apid.ConfigTunersItem) error
	LoadChannels() ([]apid.ConfigChannelsItem, error)
	SaveChannels([]apid.ConfigChannelsItem) error
}

var _ ConfigLoader = (*config.Loader)(nil)

// Options は Server の生成オプション。
type Options struct {
	// App は core.App (必須)。
	App *core.App
	// Tuner はチューナー操作。nil なら nullTuner (全ストリーム 503)。
	Tuner Tuner
	// Loader は設定ファイル入出力。nil なら config.NewLoader()。
	Loader ConfigLoader
	// System は IP 許可判定。nil なら config.NewSystem(App.Config.Server)。
	System *config.System
	// Version は Server ヘッダと /api/version current に使う。空なら App.Version。
	Version string
	// Logger はロガー。nil なら App.Log。
	Logger *mlog.Logger

	// 静的ファイル (UI)。空なら既定値。
	UIDir       string
	UIIndex     string
	RedocDir    string
	RedocTryDir string
	RedocUIHTML string

	// TestMode は listen アドレス探索をスキップする (Node 版 Server#testMode)。
	TestMode bool
	// Now は現在時刻 (テスト用)。
	Now func() time.Time
	// LatestVersion は /api/version の latest を解決する。nil なら npm view を実行する。
	LatestVersion func() (string, error)
	// Restart は PUT /api/restart の実処理。nil なら Node 版と同じ分岐を行う。
	Restart func() (int, error)
}

// Server は HTTP サーバー本体。複数の listen アドレス (TCP / unix socket) を束ねる。
type Server struct {
	app     *core.App
	tuner   Tuner
	loader  ConfigLoader
	system  *config.System
	log     *mlog.Logger
	version string
	opts    Options

	handlerOnce sync.Once
	handler     http.Handler

	scan *scanManager
	rpc  *rpcHub

	mu          sync.Mutex
	running     bool
	httpServers []*http.Server
	listeners   []net.Listener
}

// New は Server を構築する (この時点では listen しない)。
func New(opts Options) *Server {
	if opts.App == nil {
		panic("server: Options.App is required")
	}
	log := opts.Logger
	if log == nil {
		log = opts.App.Log
	}
	if log == nil {
		log = mlog.Default()
	}
	version := opts.Version
	if version == "" {
		version = opts.App.Version
	}
	if version == "" {
		version = core.DefaultVersion
	}
	loader := opts.Loader
	if loader == nil {
		loader = config.NewLoader()
	}
	sys := opts.System
	if sys == nil {
		sys = config.NewSystem(opts.App.Config.Server)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	tuner := opts.Tuner
	if tuner == nil {
		tuner = nullTuner{}
	}

	s := &Server{
		app:     opts.App,
		tuner:   tuner,
		loader:  loader,
		system:  sys,
		log:     log,
		version: version,
		opts:    opts,
	}
	s.scan = newScanManager(s)
	s.rpc = newRPCHub(s)
	return s
}

// App は基礎となる core.App を返す。
func (s *Server) App() *core.App { return s.app }

// Handler は全ルートを登録した http.Handler を返す (テストからも利用できる)。
func (s *Server) Handler() http.Handler {
	s.handlerOnce.Do(func() {
		s.handler = s.accessControl(s.cors(s.buildMux()))
	})
	return s.handler
}

// IsRunning は listen 中か返す。
func (s *Server) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// addresses は Node 版 Server#init と同じ規則で listen 先を列挙する。
// "<path>" は unix domain socket、それ以外は TCP アドレスとして扱う。
func (s *Server) addresses() []string {
	cfg := s.app.Config.Server
	var addrs []string

	if cfg.Path != nil && *cfg.Path != "" {
		addrs = append(addrs, *cfg.Path)
	}

	port := DefaultPort
	if cfg.Port != nil {
		port = *cfg.Port
	}

	if !s.opts.TestMode {
		// Node 版は IPv4 アドレスが検出できるまで待機する。開発機では検出できない場合が
		// あるため TestMode ではスキップする。arg の port が nil でも DefaultPort で listen する。
		for i := 0; i < 1; i++ {
			ips := s.system.GetIPv4AddressesForListen()
			if len(ips) > 0 {
				addrs = append(addrs, ips...)
				break
			}
			s.log.Warn("Server hasn't detected IPv4 addresses...")
		}
	}

	addrs = append(addrs, "127.0.0.1")

	if !cfg.DisableIPv6 {
		if !s.opts.TestMode {
			addrs = append(addrs, s.system.GetIPv6AddressesForListen()...)
		}
		addrs = append(addrs, "::1")
	}
	_ = port
	return addrs
}

// Init はサーバーを listen する。Node 版 Server#init の移植。
func (s *Server) Init(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return errors.New("Server is running")
	}
	s.running = true
	s.mu.Unlock()

	cfg := s.app.Config.Server
	port := DefaultPort
	if cfg.Port != nil {
		port = *cfg.Port
	}

	// unix domain socket のパスは設定値そのもので判定する。
	// (Node 版と同じく cfg.path が指定されたときだけ unix socket を listen する。
	//  先頭が "/" かどうかで判定すると、Windows のドライブ文字付きパス
	//  "J:/...sock" を TCP ホスト名として扱ってしまう。)
	socketPath := ""
	if cfg.Path != nil {
		socketPath = *cfg.Path
	}

	var errs []string
	for _, addr := range s.addresses() {
		if socketPath != "" && addr == socketPath {
			if err := s.listenUnix(addr); err != nil {
				errs = append(errs, err.Error())
			}
			continue
		}
		if err := s.listenTCP(addr, port); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(s.httpServers) == 0 {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		if len(errs) > 0 {
			return fmt.Errorf("server: failed to listen: %s", strings.Join(errs, "; "))
		}
		return errors.New("server: no listen address")
	}

	s.rpc.start()
	s.log.Info("RPC interface is enabled")
	return nil
}

func (s *Server) listenUnix(path string) error {
	if _, err := os.Stat(path); err == nil {
		_ = os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		s.log.Error("failed to listen on unix socket `%s`: %s", path, err)
		return err
	}
	_ = os.Chmod(path, 0o777)
	s.log.Info("listening on http+unix://%s", strings.ReplaceAll(path, "/", "%2F"))
	return s.serve(ln)
}

func (s *Server) listenTCP(addr string, port int) error {
	hostport := net.JoinHostPort(strings.Trim(addr, "[]"), fmt.Sprintf("%d", port))
	ln, err := net.Listen("tcp", hostport)
	if err != nil {
		s.log.Error("failed to listen on http://%s: %s", hostport, err)
		return err
	}
	s.log.Info("listening on http://%s", ln.Addr().String())
	return s.serve(ln)
}

func (s *Server) serve(ln net.Listener) error {
	hs := &http.Server{
		Handler:     s.Handler(),
		IdleTimeout: serverIdleTimeout,
		BaseContext: func(net.Listener) context.Context { return context.Background() },
	}
	s.mu.Lock()
	s.httpServers = append(s.httpServers, hs)
	s.listeners = append(s.listeners, ln)
	s.mu.Unlock()

	go func() {
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("http server error: %s", err)
		}
	}()
	return nil
}

// Shutdown は RPC と全 listen を停止する。Node 版 Server#deinit の移植。
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	servers := append([]*http.Server(nil), s.httpServers...)
	s.httpServers = nil
	s.listeners = nil
	s.running = false
	s.mu.Unlock()

	s.rpc.stop()
	var firstErr error
	for _, hs := range servers {
		if err := hs.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// now はテスト可能な現在時刻を返す。
func (s *Server) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now()
}

// staticPaths は静的ファイルの実配置を返す。空文字は「提供しない」。
func (s *Server) staticPaths() (uiDir, uiIndex, redocDir, redocTryDir, redocUIHTML string) {
	uiDir = orDefault(s.opts.UIDir, DefaultUIDir)
	uiIndex = orDefault(s.opts.UIIndex, DefaultUIIndex)
	redocDir = orDefault(s.opts.RedocDir, DefaultRedocDir)
	redocTryDir = orDefault(s.opts.RedocTryDir, DefaultRedocTryDir)
	redocUIHTML = orDefault(s.opts.RedocUIHTML, DefaultRedocUIHTML)
	return
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// absPath はリポジトリ相対の既定パスを CWD 基準で解決する (テスト用に上書き可能)。
func absPath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, p)
	}
	return p
}
