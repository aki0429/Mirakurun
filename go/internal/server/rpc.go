/*
   Copyright 2021 kanreisa
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
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/aki0429/Mirakurun/go/internal/apid"
)

// RPC のメソッド名 (Node 版 rpc.ts の methods.set と同じ並び)。
const (
	rpcMethodJoin            = "join"
	rpcMethodLeave           = "leave"
	rpcMethodGetStatus       = "getStatus"
	rpcMethodGetServices     = "getServices"
	rpcMethodGetTuners       = "getTuners"
	rpcMethodGetJobs         = "getJobs"
	rpcMethodGetJobSchedules = "getJobSchedules"
)

// rpcPingTimeout / rpcPingInterval は Node 版 jsonrpc2-ws の既定値。
const (
	rpcPingTimeout = 5 * time.Second
	rpcNotifyDelay = 8 * time.Millisecond
)

// rpcSocket は 1 本の WebSocket 接続 (Node 版 jsonrpc2-ws の Socket)。
type rpcSocket struct {
	id    string
	conn  *websocket.Conn
	write sync.Mutex
	rooms map[string]bool
	ip    string
	ua    string
}

func (sock *rpcSocket) send(b []byte) {
	sock.write.Lock()
	defer sock.write.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = sock.conn.Write(ctx, websocket.MessageText, b)
}

func (sock *rpcSocket) joinTo(room string)    { sock.rooms[room] = true }
func (sock *rpcSocket) leaveFrom(room string) { delete(sock.rooms, room) }

// rpcHub は全 WebSocket 接続とイベント/ログ通知を管理する。
type rpcHub struct {
	server *Server

	mu     sync.Mutex
	socks  map[string]*rpcSocket
	seq    int
	closed bool

	startOnce sync.Once
	pingStop  chan struct{}
}

func newRPCHub(s *Server) *rpcHub {
	return &rpcHub{server: s, socks: map[string]*rpcSocket{}}
}

// start は初期化 (通知購読と ping タイマー) を 1 度だけ行う。
func (h *rpcHub) start() {
	h.startOnce.Do(func() {
		h.initNotifiers()
		h.pingStop = make(chan struct{})
		go h.pingLoop()
	})
}

func (h *rpcHub) stop() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	socks := make([]*rpcSocket, 0, len(h.socks))
	for _, s := range h.socks {
		socks = append(socks, s)
	}
	h.mu.Unlock()

	if h.pingStop != nil {
		close(h.pingStop)
	}
	for _, s := range socks {
		_ = s.conn.Close(websocket.StatusGoingAway, "server shutdown")
	}
}

// pingLoop は 30 秒ごとに Ping を送り、応答がなければ切断する (jsonrpc2-ws の _ping)。
func (h *rpcHub) pingLoop() {
	ticker := time.NewTicker(rpcPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.pingStop:
			return
		case <-ticker.C:
			h.pingAll()
		}
	}
}

func (h *rpcHub) pingAll() {
	h.mu.Lock()
	socks := make([]*rpcSocket, 0, len(h.socks))
	for _, s := range h.socks {
		socks = append(socks, s)
	}
	h.mu.Unlock()
	for _, s := range socks {
		go func(sock *rpcSocket) {
			ctx, cancel := context.WithTimeout(context.Background(), rpcPingTimeout)
			defer cancel()
			if err := sock.conn.Ping(ctx); err != nil {
				_ = sock.conn.Close(websocket.StatusPolicyViolation, "ping timeout")
			}
		}(s)
	}
}

// notifyTo はルームに参加している全ソケットへ通知を送る。
func (h *rpcHub) notifyTo(room, method string, params any) {
	body := struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", method, params}
	b, err := marshalNoEscape(body)
	if err != nil {
		return
	}
	h.mu.Lock()
	targets := make([]*rpcSocket, 0, len(h.socks))
	for _, s := range h.socks {
		if s.rooms[room] {
			targets = append(targets, s)
		}
	}
	h.mu.Unlock()
	for _, s := range targets {
		s.send(b)
	}
}

// initNotifiers は Event / Log の購読を開始し、room ごとにバッチ通知する (rpc.ts initRPCNotifier)。
func (h *rpcHub) initNotifiers() {
	evCh, _ := h.server.app.Event.OnEvent(1024)
	eventNotifiers := map[apid.EventResource]*rpcNotifier[apid.Event]{
		apid.EventResourceProgram:     newRPCNotifier[apid.Event](h, "events:program", "events"),
		apid.EventResourceService:     newRPCNotifier[apid.Event](h, "events:service", "events"),
		apid.EventResourceTuner:       newRPCNotifier[apid.Event](h, "events:tuner", "events"),
		apid.EventResourceJob:         newRPCNotifier[apid.Event](h, "events:job", "events"),
		apid.EventResourceJobSchedule: newRPCNotifier[apid.Event](h, "events:job_schedule", "events"),
	}
	go func() {
		for ev := range evCh {
			if n, ok := eventNotifiers[ev.Resource]; ok {
				n.notify(ev)
			}
		}
	}()

	logCh, _ := h.server.app.Log.Subscribe(1024)
	logsNotifier := newRPCNotifier[string](h, "logs", "logs")
	go func() {
		for line := range logCh {
			logsNotifier.notify(line)
		}
	}()
}

// rpcNotifier は 8ms のデバウンスで items をまとめて通知する (Node 版 NotifyManager)。
type rpcNotifier[T any] struct {
	hub    *rpcHub
	room   string
	method string

	mu     sync.Mutex
	items  []T
	active bool
}

func newRPCNotifier[T any](h *rpcHub, room, method string) *rpcNotifier[T] {
	return &rpcNotifier[T]{hub: h, room: room, method: method}
}

func (n *rpcNotifier[T]) notify(item T) {
	n.mu.Lock()
	n.items = append(n.items, item)
	if n.active {
		n.mu.Unlock()
		return
	}
	n.active = true
	n.mu.Unlock()

	go func() {
		time.Sleep(rpcNotifyDelay)
		n.mu.Lock()
		items := n.items
		n.items = nil
		n.active = false
		n.mu.Unlock()

		if n.hub.server.app.Status.RPCCount() > 0 {
			n.hub.notifyTo(n.room, n.method, map[string]any{"array": items})
		}
	}()
}

// handleRPC は GET /rpc の WebSocket ハンドラ。
//
// アクセス制御 (IP / Origin) は accessControl ミドルウェアで済んでいるため、
// coder/websocket 側の Origin 検証は無効化する (Node 版 serverOnUpgrade と同じ判定をここで代替)。
func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(4 << 20)

	s.rpc.start()

	ip := clientIP(r)
	if ip == "" {
		ip = "unix"
	}
	sock := &rpcSocket{
		id:    s.rpc.nextID(),
		conn:  conn,
		rooms: map[string]bool{},
		ip:    ip,
		ua:    r.UserAgent(),
	}
	s.rpc.add(sock)
	s.app.Status.AddRPC()
	s.log.Info("%s - RPC #%s connected - - %s", ip, sock.id, sock.ua)

	defer func() {
		s.rpc.remove(sock)
		s.app.Status.AddRPCN(-1)
		s.log.Info("%s - RPC #%s closed - %s", sock.ip, sock.id, sock.ua)
	}()

	ctx := r.Context()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if resp := s.rpc.handleMessage(sock, data); resp != nil {
			sock.send(resp)
		}
	}
}

func (h *rpcHub) nextID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	return "rpc-" + strconv.Itoa(h.seq)
}

func (h *rpcHub) add(sock *rpcSocket) {
	h.mu.Lock()
	h.socks[sock.id] = sock
	h.mu.Unlock()
}

func (h *rpcHub) remove(sock *rpcSocket) {
	h.mu.Lock()
	delete(h.socks, sock.id)
	h.mu.Unlock()
	_ = sock.conn.Close(websocket.StatusNormalClosure, "")
}

// rpcError は JSON-RPC 2.0 エラーオブジェクト。
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func newRPCError(code int, message string, data any) rpcError {
	if message == "" {
		message = rpcErrorCodeMap[code]
		if message == "" {
			message = "Server error"
		}
	}
	return rpcError{Code: code, Message: message, Data: data}
}

var rpcErrorCodeMap = map[int]string{
	-32700: "Parse error",
	-32600: "Invalid Request",
	-32601: "Method not found",
	-32602: "Invalid params",
	-32603: "Internal error",
	-32000: "Server error",
}

// handleMessage は 1 メッセージを処理し、応答がある場合のみバイト列を返す (MessageHandler.handleMessage)。
func (h *rpcHub) handleMessage(sock *rpcSocket, data []byte) []byte {
	trimmed := trimLeadingSpace(data)
	if len(trimmed) == 0 {
		return errResponseRaw(nil, -32700, "", "Invalid JSON")
	}

	if trimmed[0] == '[' {
		var calls []json.RawMessage
		if err := json.Unmarshal(trimmed, &calls); err != nil {
			return errResponseRaw(nil, -32700, "", "Invalid JSON")
		}
		if len(calls) == 0 {
			return errResponseRaw(nil, -32600, "", "Empty Array")
		}
		responses := [][]byte{}
		for _, c := range calls {
			if r := h.processCall(sock, c); r != nil {
				responses = append(responses, r)
			}
		}
		if len(responses) == 0 {
			return nil
		}
		return joinJSONArray(responses)
	}

	// Node 版は先に JSON.parse するため、不正 JSON は -32700 になる。
	var probe json.RawMessage
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return errResponseRaw(nil, -32700, "", "Invalid JSON")
	}
	if !isJSONObject(probe) {
		return errResponseRaw(nil, -32600, "", nil)
	}
	return h.processCall(sock, probe)
}

// processCall は 1 リクエストを処理する。
func (h *rpcHub) processCall(sock *rpcSocket, raw []byte) []byte {
	var call map[string]json.RawMessage
	if err := json.Unmarshal(raw, &call); err != nil || call == nil {
		return errResponseRaw(nil, -32600, "", nil)
	}

	idRaw, hasID := call["id"]
	var idOut any = nil
	if hasID && string(idRaw) != "null" {
		idOut = json.RawMessage(idRaw)
	}

	// jsonrpc バージョン (STRICT)
	var version string
	if v, ok := call["jsonrpc"]; ok {
		_ = json.Unmarshal(v, &version)
	}
	if version != "2.0" {
		return errResponse(idOut, -32600, "", "Invalid JSON-RPC Version")
	}

	// 応答オブジェクト (result / error を持つ) は無視する。
	_, hasResult := call["result"]
	_, hasError := call["error"]
	if hasID && (hasResult || hasError) {
		if idOut == nil && !hasError {
			return errResponse(idOut, -32600, "", nil)
		}
		return nil
	}

	methodRaw, hasMethod := call["method"]
	if !hasMethod {
		return errResponse(idOut, -32601, "", "Method not specified")
	}
	var method string
	if err := json.Unmarshal(methodRaw, &method); err != nil {
		return errResponse(idOut, -32600, "", "Invalid type of method name")
	}

	if paramsRaw, ok := call["params"]; ok {
		if !isJSONObject(paramsRaw) && !isJSONArray(paramsRaw) {
			return errResponse(idOut, -32600, "", nil)
		}
	}

	handler := h.methodTable(sock)[method]
	if handler == nil {
		return errResponse(idOut, -32601, "", nil)
	}

	result, err := handler(call["params"])
	if err != nil {
		if !hasID {
			return nil
		}
		return resultOrError(idOut, nil, newRPCError(-32000, "Error", err.Error()))
	}
	if !hasID {
		return nil
	}
	if result == nil {
		result = nil
	}
	return resultOrError(idOut, result, rpcError{})
}

// methodTable はメソッド名 → ハンドラを返す。join/leave はソケットに束縛される。
func (h *rpcHub) methodTable(sock *rpcSocket) map[string]func(params json.RawMessage) (any, error) {
	return map[string]func(json.RawMessage) (any, error){
		rpcMethodJoin: func(params json.RawMessage) (any, error) {
			var p struct {
				Rooms []string `json:"rooms"`
			}
			if len(params) > 0 {
				_ = json.Unmarshal(params, &p)
			}
			for _, room := range p.Rooms {
				sock.joinTo(room)
			}
			return nil, nil
		},
		rpcMethodLeave: func(params json.RawMessage) (any, error) {
			var p struct {
				Rooms []string `json:"rooms"`
			}
			if len(params) > 0 {
				_ = json.Unmarshal(params, &p)
			}
			for _, room := range p.Rooms {
				sock.leaveFrom(room)
			}
			return nil, nil
		},
		rpcMethodGetStatus: func(json.RawMessage) (any, error) {
			return h.server.app.GetStatus(), nil
		},
		rpcMethodGetServices: func(json.RawMessage) (any, error) {
			out := []apid.Service{}
			for _, svc := range h.server.sortedServiceItems() {
				out = append(out, h.server.exportServiceWithLogoData(svc))
			}
			return out, nil
		},
		rpcMethodGetTuners: func(json.RawMessage) (any, error) {
			return h.server.tuner.Devices(), nil
		},
		rpcMethodGetJobs: func(json.RawMessage) (any, error) {
			return h.server.app.Job.Jobs(), nil
		},
		rpcMethodGetJobSchedules: func(json.RawMessage) (any, error) {
			return h.server.app.Job.Schedules(), nil
		},
	}
}

// ---- JSON ヘルパ ----

func trimLeadingSpace(b []byte) []byte {
	for len(b) > 0 {
		switch b[0] {
		case ' ', '\t', '\r', '\n':
			b = b[1:]
		default:
			return b
		}
	}
	return b
}

func isJSONObject(b []byte) bool {
	t := trimLeadingSpace(b)
	return len(t) > 0 && t[0] == '{'
}

func isJSONArray(b []byte) bool {
	t := trimLeadingSpace(b)
	return len(t) > 0 && t[0] == '['
}

func joinJSONArray(parts [][]byte) []byte {
	out := make([]byte, 0, 2)
	out = append(out, '[')
	for i, p := range parts {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, p...)
	}
	out = append(out, ']')
	return out
}

// resultOrError は成功/失敗のレスポンスを組み立てる。
func resultOrError(id any, result any, e rpcError) []byte {
	if e.Code != 0 {
		body := struct {
			JSONRPC string   `json:"jsonrpc"`
			ID      any      `json:"id"`
			Error   rpcError `json:"error"`
		}{"2.0", id, e}
		b, _ := marshalNoEscape(body)
		return b
	}
	body := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      any    `json:"id"`
		Result  any    `json:"result"`
	}{"2.0", id, result}
	b, _ := marshalNoEscape(body)
	return b
}

// errResponseRaw は id なしエラー (parse error 等) を組み立てる。
func errResponseRaw(id any, code int, message string, data any) []byte {
	return errResponse(id, code, message, data)
}

func errResponse(id any, code int, message string, data any) []byte {
	body := struct {
		JSONRPC string   `json:"jsonrpc"`
		ID      any      `json:"id"`
		Error   rpcError `json:"error"`
	}{"2.0", id, newRPCError(code, message, data)}
	b, _ := marshalNoEscape(body)
	return b
}
