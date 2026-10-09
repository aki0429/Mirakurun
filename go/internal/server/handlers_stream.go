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
	"io"
	"net"
	"net/http"
	"strconv"
)

// tunerUserID は Node 版 `(req.ip || "unix") + ":" + (req.socket.remotePort || Date.now())`。
func (s *Server) tunerUserID(r *http.Request) string {
	ip := clientIP(r)
	if ip == "" {
		ip = "unix"
	}
	_, port, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || port == "" {
		port = strconv.FormatInt(s.now().UnixMilli(), 10)
	}
	return ip + ":" + port
}

// streamPriority は X-Mirakurun-Priority ヘッダを解析する (parseInt(..., 10) || 0)。
func streamPriority(r *http.Request) int {
	v := r.Header.Get("X-Mirakurun-Priority")
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

// serveStream は 3 つのストリーム系ハンドラが共有する本体。
//
// Node 版の流れを再現する:
//   - HEAD はチューナーを起動せず Content-Type: video/MP2T と X-Mirakurun-Tuner-User-ID を付けて 200。
//   - GET は Init*Stream を呼び、成功したら (最初のバイトが出る前に) 200 とヘッダを確定する。
//   - 失敗時は responseStreamErrorHandler (no available tuners → 503 Tuner Resource Unavailable)。
//   - クライアント切断時に TSFilter を閉じる。
func (s *Server) serveStream(w http.ResponseWriter, r *http.Request, init func(req UserRequest, out io.Writer) (TSFilter, error)) {
	userId := s.tunerUserID(r)

	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "video/MP2T")
		w.Header().Set("X-Mirakurun-Tuner-User-ID", userId)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Node 版 disableDecoder は `req.query.decode === 0`。Express はクエリを文字列で渡すため
	// 常に false になる。挙動互換のため false 固定 (既定のデコーダ有効)。
	req := UserRequest{
		ID:             userId,
		Priority:       streamPriority(r),
		Agent:          r.UserAgent(),
		URL:            r.URL.RequestURI(),
		DisableDecoder: false,
	}

	lw := &lazyStreamWriter{w: w, userId: userId, flusher: flusherOf(w)}
	filter, err := init(req, lw)
	if err != nil {
		if !lw.started {
			s.responseStreamErrorHandler(w, err)
		}
		return
	}
	if !lw.started {
		lw.start()
	}

	// クライアント切断時にフィルタを閉じる。
	if filter != nil {
		go func() {
			<-r.Context().Done()
			_ = filter.Close()
		}()
	}
}

// lazyStreamWriter は「初回書き込み時にヘッダと 200 を確定する」io.Writer。
// Node 版が res.write 開始時に Content-Type 等を送る挙動を再現する。
type lazyStreamWriter struct {
	w       http.ResponseWriter
	userId  string
	flusher http.Flusher
	started bool
}

func flusherOf(w http.ResponseWriter) http.Flusher {
	if f, ok := w.(http.Flusher); ok {
		return f
	}
	return nil
}

func (l *lazyStreamWriter) start() {
	if l.started {
		return
	}
	l.started = true
	l.w.Header().Set("Content-Type", "video/MP2T")
	l.w.Header().Set("X-Mirakurun-Tuner-User-ID", l.userId)
	l.w.WriteHeader(http.StatusOK)
	if l.flusher != nil {
		l.flusher.Flush()
	}
}

func (l *lazyStreamWriter) Write(p []byte) (int, error) {
	if !l.started {
		l.start()
	}
	n, err := l.w.Write(p)
	if l.flusher != nil {
		l.flusher.Flush()
	}
	return n, err
}
