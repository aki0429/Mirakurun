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

package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// buildMux は Node 版 express-openapi が読み込む src/Mirakurun/api/** の全ルートを登録する。
// "GET " パターンは net/http が HEAD も自動で受けるため、ストリーム系はハンドラ側で
// r.Method == HEAD を分岐して Node 版と同じ「チューナーを起動しない 200」を返す。
func (s *Server) buildMux() *http.ServeMux {
	m := http.NewServeMux()

	// channels (src/Mirakurun/api/channels*.ts)
	m.HandleFunc("GET /api/channels", s.handleChannels)
	m.HandleFunc("GET /api/channels/{type}", s.handleChannelsByType)
	m.HandleFunc("GET /api/channels/{type}/{channel}", s.handleChannel)
	m.HandleFunc("GET /api/channels/{type}/{channel}/services", s.handleChannelServices)
	m.HandleFunc("GET /api/channels/{type}/{channel}/services/{id}", s.handleChannelService)
	m.HandleFunc("GET /api/channels/{type}/{channel}/services/{id}/stream", s.handleChannelServiceStream)
	m.HandleFunc("GET /api/channels/{type}/{channel}/stream", s.handleChannelStream)

	// services (src/Mirakurun/api/services*.ts)
	m.HandleFunc("GET /api/services", s.handleServices)
	m.HandleFunc("GET /api/services/{id}", s.handleService)
	m.HandleFunc("GET /api/services/{id}/logo", s.handleServiceLogo)
	m.HandleFunc("GET /api/services/{id}/stream", s.handleServiceStream)

	// programs (src/Mirakurun/api/programs*.ts)
	m.HandleFunc("GET /api/programs", s.handlePrograms)
	m.HandleFunc("GET /api/programs/{id}", s.handleProgram)
	m.HandleFunc("GET /api/programs/{id}/stream", s.handleProgramStream)

	// events / log / status / version
	m.HandleFunc("GET /api/events", s.handleEvents)
	m.HandleFunc("GET /api/events/stream", s.handleEventsStream)
	m.HandleFunc("GET /api/log", s.handleLog)
	m.HandleFunc("GET /api/log/stream", s.handleLogStream)
	m.HandleFunc("GET /api/status", s.handleStatus)
	m.HandleFunc("GET /api/version", s.handleVersion)

	// tuners
	m.HandleFunc("GET /api/tuners", s.handleTuners)
	m.HandleFunc("GET /api/tuners/{index}", s.handleTuner)
	m.HandleFunc("GET /api/tuners/{index}/process", s.handleTunerProcess)
	m.HandleFunc("DELETE /api/tuners/{index}/process", s.handleKillTunerProcess)

	// jobs / job-schedules
	m.HandleFunc("GET /api/jobs", s.handleJobs)
	m.HandleFunc("PUT /api/jobs/{id}/abort", s.handleJobAbort)
	m.HandleFunc("PUT /api/jobs/{id}/rerun", s.handleJobRerun)
	m.HandleFunc("GET /api/job-schedules", s.handleJobSchedules)
	m.HandleFunc("PUT /api/job-schedules/{key}/run", s.handleJobScheduleRun)

	// config
	m.HandleFunc("GET /api/config/server", s.handleGetServerConfig)
	m.HandleFunc("PUT /api/config/server", s.handlePutServerConfig)
	m.HandleFunc("GET /api/config/tuners", s.handleGetTunersConfig)
	m.HandleFunc("PUT /api/config/tuners", s.handlePutTunersConfig)
	m.HandleFunc("GET /api/config/channels", s.handleGetChannelsConfig)
	m.HandleFunc("PUT /api/config/channels", s.handlePutChannelsConfig)
	m.HandleFunc("GET /api/config/channels/scan", s.handleGetChannelScan)
	m.HandleFunc("PUT /api/config/channels/scan", s.handlePutChannelScan)
	m.HandleFunc("DELETE /api/config/channels/scan", s.handleDeleteChannelScan)

	// iptv
	m.HandleFunc("GET /api/iptv/discover.json", s.handleIPTVDiscover)
	m.HandleFunc("GET /api/iptv/lineup.json", s.handleIPTVLineup)
	m.HandleFunc("GET /api/iptv/lineup_status.json", s.handleIPTVLineupStatus)
	m.HandleFunc("GET /api/iptv/playlist", s.handleIPTVPlaylist)
	m.HandleFunc("GET /api/iptv/xmltv", s.handleIPTVXMLTV)

	// restart
	m.HandleFunc("PUT /api/restart", s.handleRestart)

	// JSON-RPC 2.0 over WebSocket (Server.ts は全 listen サーバーの upgrade に /rpc を割り当てる)
	m.HandleFunc("GET /rpc", s.handleRPC)

	// 静的 UI (Server.ts は disableWebUI でないときのみ提供する)
	if !s.webUIDisabled() {
		uiDir, uiIndex, redocDir, redocTryDir, redocUIHTML := s.staticPaths()
		m.Handle("/redoc/", http.StripPrefix("/redoc/", http.FileServer(http.Dir(absPath(redocDir)))))
		m.Handle("/redoc-try/", http.StripPrefix("/redoc-try/", http.FileServer(http.Dir(absPath(redocTryDir)))))
		m.HandleFunc("/api/debug", s.serveStaticFile(absPath(redocUIHTML)))
		m.Handle("/", s.uiHandler(absPath(uiDir), absPath(uiIndex)))
	}

	return m
}

// webUIDisabled は config.server.disableWebUI を返す。
func (s *Server) webUIDisabled() bool {
	cfg := s.app.Config.Server
	return cfg != nil && cfg.DisableWebUI
}

// uiHandler は express.static("lib/ui") + SPA フォールバック (index.html) を再現する。
// /api, /assets, /redoc 系以外のパスで実ファイルが無い場合は index.html を返す。
func (s *Server) uiHandler(uiDir, uiIndex string) http.Handler {
	fs := http.FileServer(http.Dir(uiDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/" {
			s.serveStaticFile(uiIndex)(w, r)
			return
		}
		// 実ファイルがあればそれを返す。
		if file, err := os.Stat(filepath.Join(uiDir, filepath.FromSlash(strings.TrimPrefix(p, "/")))); err == nil && !file.IsDir() {
			fs.ServeHTTP(w, r)
			return
		}
		// SPA ルート: /api|/assets|/redoc は除外し、それ以外は index.html を返す。
		if hasAnyPrefix([]string{"/api/", "/assets/", "/redoc/", "/redoc-try/"}, p) {
			http.NotFound(w, r)
			return
		}
		s.serveStaticFile(uiIndex)(w, r)
	})
}

// serveStaticFile は 1 ファイルを返す。存在しなければ 404。
func (s *Server) serveStaticFile(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(path); err != nil {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, path)
	}
}
