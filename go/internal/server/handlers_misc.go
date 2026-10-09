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
	"context"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
)

// handleStatus は GET /api/status (src/Mirakurun/api/status.ts)。
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.responseJSON(w, s.app.GetStatus())
}

// handleVersion は GET /api/version (src/Mirakurun/api/version.ts)。
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	latest, err := s.latestVersion(r.Context())
	if err != nil {
		s.responseError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.responseJSON(w, apid.Version{Current: s.version, Latest: latest})
}

// latestVersion は Node 版 system.getLatestVersion (`npm view mirakurun version`) を再現する。
func (s *Server) latestVersion(ctx context.Context) (string, error) {
	if s.opts.LatestVersion != nil {
		return s.opts.LatestVersion()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "npm", "view", "mirakurun", "version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// handleEvents は GET /api/events (src/Mirakurun/api/events.ts)。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	s.responseJSON(w, s.app.Event.Log())
}

// handleEventsStream は GET /api/events/stream (src/Mirakurun/api/events/stream.ts)。
//
// Node 版は "[\n" を書いた後、発火したイベントを `JSON.stringify(ev) + "\n,\n"` で追記し続ける。
// バックログは送らない (後から接続したクライアントは "[\n" だけを受け取る)。この挙動をそのまま再現する。
func (s *Server) handleEventsStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	_, _ = w.Write([]byte("[\n"))
	if flusher != nil {
		flusher.Flush()
	}

	resource := r.URL.Query().Get("resource")
	typ := r.URL.Query().Get("type")

	ch, cancel := s.app.Event.OnEvent(256)
	defer cancel()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if resource != "" && string(ev.Resource) != resource {
				continue
			}
			if typ != "" && string(ev.Type) != typ {
				continue
			}
			b, err := marshalNoEscape(ev)
			if err != nil {
				continue
			}
			if _, err := w.Write(append(append(b, '\n'), ',', '\n')); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// handleLog は GET /api/log (src/Mirakurun/api/log.ts)。
func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	for _, line := range s.app.Log.History() {
		_, _ = w.Write([]byte(line + "\n"))
	}
}

// handleLogStream は GET /api/log/stream (src/Mirakurun/api/log/stream.ts)。
func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	ch, cancel := s.app.Log.Subscribe(256)
	defer cancel()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write([]byte(line + "\n")); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// handleRestart は PUT /api/restart (src/Mirakurun/api/restart.ts)。
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if s.opts.Restart != nil {
		pid, err := s.opts.Restart()
		if err != nil {
			s.responseError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.restartAccepted(w, pid)
		return
	}

	if _, ok := os.LookupEnv("pm_uptime"); ok && os.Getenv("pm_uptime") != "" {
		cmd := exec.Command("mirakurun", "restart")
		cmd.Stdout = nil
		cmd.Stderr = nil
		if err := cmd.Start(); err != nil {
			s.responseError(w, http.StatusInternalServerError, err.Error())
			return
		}
		pid := cmd.Process.Pid
		_ = cmd.Process.Release()
		s.restartAccepted(w, pid)
		return
	}

	if os.Getenv("DOCKER") == "YES" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"_exit":0}`))
		if initPID := os.Getenv("INIT_PID"); initPID != "" {
			if pid, err := strconv.Atoi(initPID); err == nil {
				go func() {
					if p, err := os.FindProcess(pid); err == nil {
						_ = p.Signal(sigHUP())
					}
				}()
			}
		}
		return
	}

	s.responseError(w, http.StatusInternalServerError)
}

func (s *Server) restartAccepted(w http.ResponseWriter, pid int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"_cmd_pid":` + itoa(pid) + `}`))
}
