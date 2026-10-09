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
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/aki0429/Mirakurun/go/internal/apid"
)

// clientIP は Node 版 req.ip (== req.socket.remoteAddress) 相当の IP を返す。
// unix domain socket など IP でない場合は空文字を返し、呼び出し側はアクセス制御をスキップする
// (Node 版も req.ip が undefined のときチェックしない)。
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if net.ParseIP(host) == nil {
		return ""
	}
	return host
}

// accessControl は Server.ts のアクセス制御ミドルウェアを移植する。
//   - 許可されない IP は接続ごと切断する (Node 版 req.socket.end())。
//   - Origin / Referer が許可されない場合は 403 を返す。
//   - 成功時はセキュリティヘッダ (X-Your-IP 等) を付与して next を呼ぶ。
func (s *Server) accessControl(next http.Handler) http.Handler {
	cfg := s.app.Config.Server
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if ip != "" && !s.system.IsPermittedIPAddress(ip) {
			s.closeConn(w)
			return
		}

		if origin := r.Header.Get("Origin"); origin != "" {
			if !s.system.IsPermittedHost(origin, cfg.Hostname) && !containsString(cfg.AllowOrigins, origin) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
		}

		if referer := r.Header.Get("Referer"); referer != "" {
			if !s.system.IsPermittedHost(referer, cfg.Hostname) && !hasAnyPrefix(cfg.AllowOrigins, referer) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
		}

		allowPNA := cfg.AllowPNA != nil && *cfg.AllowPNA
		if allowPNA && r.Header.Get("Access-Control-Request-Method") != "" &&
			r.Header.Get("Access-Control-Request-Private-Network") == "true" {
			h := w.Header()
			h.Set("Access-Control-Allow-Private-Network", "true")
			h.Set("Private-Network-Access-Name", "Mirakurun_"+cfg.Hostname)
			h.Set("Private-Network-Access-ID", "00:00:00:00:00")
		}

		h := w.Header()
		h.Set("Cross-Origin-Resource-Policy", "cross-origin")
		h.Set("Cross-Origin-Embedder-Policy", "require-corp")
		h.Set("Server", "Mirakurun/"+s.version)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Your-IP", ip)

		next.ServeHTTP(w, r)
	})
}

// closeConn は接続を応答なしで切断する (Node 版 req.socket.end())。
func (s *Server) closeConn(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			_ = conn.Close()
			return
		}
	}
	// ハイジャックできない場合は net/http に接続を中断させる。
	panic(http.ErrAbortHandler)
}

// cors は Node 版 `app.use(cors())` (既定オプション) を再現する。
//   - すべての応答に Access-Control-Allow-Origin: * を付ける。
//   - プリフライト (OPTIONS + Access-Control-Request-Method) は 204 で打ち切る。
//   - Access-Control-Allow-Headers はリクエストの Access-Control-Request-Headers をそのまま返す。
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")

		if r.Method == http.MethodOptions && h.Get("Access-Control-Allow-Methods") == "" {
			if r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", "GET,HEAD,PUT,PATCH,POST,DELETE")
				if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
					h.Set("Access-Control-Allow-Headers", reqHeaders)
					h.Add("Vary", "Access-Control-Request-Headers")
				}
				h.Set("Content-Length", "0")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// marshalNoEscape は Node 版 JSON.stringify と同じく HTML エスケープ (<, >, &) を行わない。
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	return b, nil
}

// responseJSON は Node 版 api.ts responseJSON の移植。
// Content-Type は application/json; charset=utf-8、ステータスは常に 200。
func (s *Server) responseJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	b, err := marshalNoEscape(body)
	if err != nil {
		s.log.Error("failed to marshal JSON response: %s", err)
		b = []byte("null")
	}
	_, _ = w.Write(b)
}

// responseError は Node 版 api.ts responseError の移植。
// ボディは凍結形 {code, reason|null, errors: []}、Content-Type は application/json。
func (s *Server) responseError(w http.ResponseWriter, code int, reason ...string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)

	var rp *string
	if len(reason) > 0 && reason[0] != "" {
		rp = &reason[0]
	}
	body := apid.Error{Code: code, Reason: rp, Errors: []apid.ErrorOfOpenAPI{}}
	b, err := marshalNoEscape(body)
	if err != nil {
		b = []byte(`{"code":` + itoa(code) + `,"reason":null,"errors":[]}`)
	}
	_, _ = w.Write(b)
}

// responseStreamErrorHandler は Node 版 api.ts responseStreamErrorHandler の移植。
func (s *Server) responseStreamErrorHandler(w http.ResponseWriter, err error) {
	if err != nil && err.Error() == "no available tuners" {
		s.responseError(w, http.StatusServiceUnavailable, "Tuner Resource Unavailable")
		return
	}
	msg := "unknown error"
	if err != nil {
		msg = err.Error()
	}
	s.responseError(w, http.StatusInternalServerError, msg)
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func hasAnyPrefix(list []string, v string) bool {
	for _, x := range list {
		if strings.HasPrefix(v, x) {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
