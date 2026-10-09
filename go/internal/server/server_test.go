/*
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
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/config"
	"github.com/aki0429/Mirakurun/go/internal/core"
	"github.com/aki0429/Mirakurun/go/internal/db"
	"github.com/aki0429/Mirakurun/go/internal/event"
	"github.com/aki0429/Mirakurun/go/internal/job"
	"github.com/aki0429/Mirakurun/go/internal/mlog"
)

// ---- テスト用スタブ ----

func quietLogger() *mlog.Logger {
	return mlog.New(mlog.Options{Stdout: io.Discard, Stderr: io.Discard})
}

// stubFilter は TSFilter のテスト実装。
type stubFilter struct {
	closed bool
	drops  int64
}

func (f *stubFilter) Close() error { f.closed = true; return nil }
func (f *stubFilter) Drops() int64 { return f.drops }

// stubDevice は TunerDevice のテスト実装。
type stubDevice struct {
	idx    int
	name   string
	pid    *int
	killed bool
}

func (d *stubDevice) Status() apid.TunerDevice {
	return apid.TunerDevice{
		Index:       d.idx,
		Name:        d.name,
		Types:       []apid.ChannelType{apid.ChannelTypeGR},
		PID:         d.pid,
		Users:       []apid.TunerUser{},
		IsAvailable: true,
		IsFree:      true,
	}
}
func (d *stubDevice) KillProcess() { d.killed = true }

// stubTuner は server.Tuner と core.TunerAPI の両方を満たす。
type stubTuner struct {
	types    map[apid.ChannelType]bool
	devices  []*stubDevice
	services []apid.Service

	streamData []byte
	initErr    error

	mu          sync.Mutex
	initCalls   int
	lastReq     UserRequest
	lastService *core.ServiceItem

	// currentOutput は Init*Stream 呼び出しから begin へ出力先を渡すための一時フィールド。
	currentOutput io.Writer
}

func newStubTuner() *stubTuner {
	return &stubTuner{
		types: map[apid.ChannelType]bool{apid.ChannelTypeGR: true, apid.ChannelTypeBS: true, apid.ChannelTypeCS: true, apid.ChannelTypeSKY: true},
	}
}

func (s *stubTuner) TypeExists(t apid.ChannelType) bool                  { return s.types[t] }
func (s *stubTuner) ReadyForJob(context.Context, *core.ChannelItem) bool { return true }
func (s *stubTuner) GetServices(context.Context, *core.ChannelItem) ([]apid.Service, error) {
	return s.services, nil
}
func (s *stubTuner) GetEPG(context.Context, *core.ChannelItem, time.Duration) error { return nil }

func (s *stubTuner) Devices() []apid.TunerDevice {
	out := make([]apid.TunerDevice, 0, len(s.devices))
	for _, d := range s.devices {
		out = append(out, d.Status())
	}
	return out
}

func (s *stubTuner) Get(index int) TunerDevice {
	for _, d := range s.devices {
		if d.idx == index {
			return d
		}
	}
	return nil
}

func (s *stubTuner) DeviceCount() int { return len(s.devices) }

func (s *stubTuner) begin(req UserRequest) (TSFilter, error) {
	s.mu.Lock()
	s.initCalls++
	s.lastReq = req
	s.mu.Unlock()
	if s.initErr != nil {
		return nil, s.initErr
	}
	if len(s.streamData) > 0 {
		if _, err := s.currentOutput.Write(s.streamData); err != nil {
			return nil, err
		}
	}
	return &stubFilter{}, nil
}

// currentOutput は begin の直前に設定される出力先。
func (s *stubTuner) InitChannelStream(_ *core.ChannelItem, req UserRequest, out io.Writer) (TSFilter, error) {
	s.currentOutput = out
	return s.begin(req)
}
func (s *stubTuner) InitServiceStream(svc *core.ServiceItem, req UserRequest, out io.Writer) (TSFilter, error) {
	s.mu.Lock()
	s.lastService = svc
	s.mu.Unlock()
	s.currentOutput = out
	return s.begin(req)
}
func (s *stubTuner) InitProgramStream(_ *apid.Program, req UserRequest, out io.Writer) (TSFilter, error) {
	s.currentOutput = out
	return s.begin(req)
}

// stubLoader は ConfigLoader のインメモリ実装。
type stubLoader struct {
	mu       sync.Mutex
	server   *apid.ConfigServer
	tuners   []apid.ConfigTunersItem
	channels []apid.ConfigChannelsItem
}

func (l *stubLoader) LoadServer() (*apid.ConfigServer, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.server == nil {
		return &apid.ConfigServer{}, nil
	}
	return l.server, nil
}
func (l *stubLoader) SaveServer(c *apid.ConfigServer) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.server = c
	return nil
}
func (l *stubLoader) LoadTuners() ([]apid.ConfigTunersItem, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tuners, nil
}
func (l *stubLoader) SaveTuners(t []apid.ConfigTunersItem) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tuners = t
	return nil
}
func (l *stubLoader) LoadChannels() ([]apid.ConfigChannelsItem, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.channels, nil
}
func (l *stubLoader) SaveChannels(c []apid.ConfigChannelsItem) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.channels = c
	return nil
}

// ---- テスト環境 ----

type testEnv struct {
	server *Server
	app    *core.App
	tuner  *stubTuner
	loader *stubLoader
	clock  *job.FakeClock
	cfg    *core.Config
}

// newTestEnv は temp DB + スタブで Server を組み立て、services.json を書き込んで Load する。
func newTestEnv(t *testing.T, tuner *stubTuner, channels []apid.ConfigChannelsItem, services []apid.Service, mutate func(*core.Config)) *testEnv {
	t.Helper()
	dir := t.TempDir()
	srvCfg := &apid.ConfigServer{
		Hostname:            "localhost",
		AllowIPv4CidrRanges: []string{"127.0.0.0/8"},
		AllowIPv6CidrRanges: []string{"::1/128"},
	}
	cfg := &core.Config{
		Server:            srvCfg,
		Channels:          channels,
		ChannelsIntegrity: "test-integrity",
		ServicesDBPath:    filepath.Join(dir, "services.json"),
		ProgramsDBPath:    filepath.Join(dir, "programs.json"),
		LogoDataDirPath:   filepath.Join(dir, "logo"),
	}
	if mutate != nil {
		mutate(cfg)
	}
	clock := job.NewFakeClock(time.Unix(1_700_000_000, 0))
	ev := event.New(event.Options{Now: func() time.Time { return clock.Now() }})
	app := core.New(core.AppOptions{Config: cfg, Tuner: tuner, Clock: clock, Log: quietLogger(), Event: ev, Version: "4.1.5"})

	if services != nil {
		if err := db.SaveServices(cfg.ServicesDBPath, services, cfg.ChannelsIntegrity); err != nil {
			t.Fatalf("SaveServices: %v", err)
		}
	}
	if err := app.Load(); err != nil {
		t.Fatalf("app.Load: %v", err)
	}

	loader := &stubLoader{server: srvCfg, channels: cfg.Channels}
	s := New(Options{
		App:      app,
		Tuner:    tuner,
		Loader:   loader,
		System:   config.NewSystem(srvCfg),
		Version:  "4.1.5",
		Logger:   quietLogger(),
		TestMode: true,
		Now:      func() time.Time { return clock.Now() },
	})
	return &testEnv{server: s, app: app, tuner: tuner, loader: loader, clock: clock, cfg: cfg}
}

// grChannels は最小の GR チャンネル構成。
func grChannels() []apid.ConfigChannelsItem {
	return []apid.ConfigChannelsItem{
		{Name: "NHK", Type: apid.ChannelTypeGR, Channel: "T13"},
		{Name: "ABC", Type: apid.ChannelTypeGR, Channel: "T15"},
	}
}

func sampleServices() []apid.Service {
	return []apid.Service{
		{ID: core.GetServiceItemID(32721, 1), ServiceID: 1, NetworkID: 32721, Name: "NHK総合", Type: apid.Ptr(1), LogoID: apid.Ptr(3), RemoteControlKeyID: apid.Ptr(1), EPGReady: apid.Ptr(true), EPGUpdatedAt: apid.Ptr(int64(1791389715615)), Channel: &apid.ServiceChannel{Type: apid.ChannelTypeGR, Channel: "T13"}},
		{ID: core.GetServiceItemID(32723, 2), ServiceID: 2, NetworkID: 32723, Name: "ABCテレビ", Type: apid.Ptr(1), RemoteControlKeyID: apid.Ptr(2), EPGReady: apid.Ptr(true), EPGUpdatedAt: apid.Ptr(int64(1791486809731)), Channel: &apid.ServiceChannel{Type: apid.ChannelTypeGR, Channel: "T15"}},
	}
}

// do は RemoteAddr を 127.0.0.1 にしてハンドラを直接叩く。
func do(t *testing.T, h http.Handler, method, target string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = "127.0.0.1:34567"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func decodeJSON(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("json.Unmarshal(%q): %v", string(b), err)
	}
}

const frozenErrorBody = `{"code":404,"reason":null,"errors":[]}`

// ---- テスト ----

func TestErrorBodyShapeAndContentType(t *testing.T) {
	tuner := newStubTuner()
	env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
	h := env.server.Handler()

	cases := []struct {
		method, target string
		wantStatus     int
		wantBody       string
	}{
		{"GET", "/api/channels/GR/NOPE", 404, frozenErrorBody},
		{"GET", "/api/services/999999", 404, `{"code":404,"reason":null,"errors":[]}`},
		{"GET", "/api/programs/999999", 404, `{"code":404,"reason":null,"errors":[]}`},
		{"GET", "/api/tuners/9", 404, `{"code":404,"reason":null,"errors":[]}`},
		{"GET", "/api/programs?%24where=%7B%7D", 400, `{"code":400,"reason":null,"errors":[]}`},
		{"GET", "/api/services?%24where=x", 400, `{"code":400,"reason":null,"errors":[]}`},
		{"GET", "/api/channels?%24where=x", 400, `{"code":400,"reason":null,"errors":[]}`},
	}
	for _, c := range cases {
		w := do(t, h, c.method, c.target, nil, nil)
		if w.Code != c.wantStatus {
			t.Errorf("%s %s: status = %d, want %d (body=%s)", c.method, c.target, w.Code, c.wantStatus, w.Body.String())
		}
		if got := w.Body.String(); got != c.wantBody {
			t.Errorf("%s %s: body = %s, want %s", c.method, c.target, got, c.wantBody)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s %s: Content-Type = %q, want application/json", c.method, c.target, ct)
		}
	}
}

func TestChannelsEndpoints(t *testing.T) {
	tuner := newStubTuner()
	env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
	h := env.server.Handler()

	w := do(t, h, "GET", "/api/channels", nil, nil)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var channels []map[string]any
	decodeJSON(t, w.Body.Bytes(), &channels)
	if len(channels) != 2 {
		t.Fatalf("channels len = %d, want 2 (%s)", len(channels), w.Body.String())
	}
	if channels[0]["type"] != "GR" || channels[0]["channel"] != "T13" {
		t.Errorf("channel[0] = %v", channels[0])
	}
	// services は常に配列で、{id,serviceId,networkId,name,type} のみ。
	svcs, ok := channels[0]["services"].([]any)
	if !ok || len(svcs) != 1 {
		t.Fatalf("services = %v", channels[0]["services"])
	}
	svc0 := svcs[0].(map[string]any)
	for _, k := range []string{"id", "serviceId", "networkId", "name", "type"} {
		if _, ok := svc0[k]; !ok {
			t.Errorf("channel service missing key %q: %v", k, svc0)
		}
	}
	if _, ok := svc0["hasLogoData"]; ok {
		t.Errorf("channel service must not include hasLogoData: %v", svc0)
	}

	// ?type=GR filter
	w = do(t, h, "GET", "/api/channels?type=GR", nil, nil)
	if w.Code != 200 {
		t.Fatalf("filtered status = %d", w.Code)
	}
	// ?name=ABC
	w = do(t, h, "GET", "/api/channels?name=ABC", nil, nil)
	var filtered []map[string]any
	decodeJSON(t, w.Body.Bytes(), &filtered)
	if len(filtered) != 1 || filtered[0]["channel"] != "T15" {
		t.Errorf("name filter = %v", filtered)
	}

	// /api/channels/{type}/{channel}
	w = do(t, h, "GET", "/api/channels/GR/T13", nil, nil)
	if w.Code != 200 {
		t.Fatalf("single channel status = %d", w.Code)
	}
	// invalid type -> 400
	if w := do(t, h, "GET", "/api/channels/XX/T13", nil, nil); w.Code != 400 {
		t.Errorf("invalid type status = %d, want 400", w.Code)
	}
	// redirects
	w = do(t, h, "GET", "/api/channels/GR/T13/services", nil, nil)
	if w.Code != 307 || w.Header().Get("Location") != "/api/services?channel.type=GR&channel.channel=T13" {
		t.Errorf("services redirect = %d %q", w.Code, w.Header().Get("Location"))
	}
	w = do(t, h, "GET", "/api/channels/GR/T13/services/1", nil, nil)
	if w.Code != 307 || !strings.HasPrefix(w.Header().Get("Location"), "/api/services/") {
		t.Errorf("service redirect = %d %q", w.Code, w.Header().Get("Location"))
	}
}

func TestServicesEndpoints(t *testing.T) {
	tuner := newStubTuner()
	env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
	h := env.server.Handler()

	w := do(t, h, "GET", "/api/services", nil, nil)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	var services []map[string]any
	decodeJSON(t, w.Body.Bytes(), &services)
	if len(services) != 2 {
		t.Fatalf("services len = %d", len(services))
	}
	if _, ok := services[0]["hasLogoData"]; !ok {
		t.Errorf("hasLogoData missing: %v", services[0])
	}
	if services[0]["hasLogoData"] != false {
		t.Errorf("hasLogoData = %v, want false", services[0]["hasLogoData"])
	}

	// filter by channel.channel
	w = do(t, h, "GET", "/api/services?channel.channel=T15", nil, nil)
	var filtered []map[string]any
	decodeJSON(t, w.Body.Bytes(), &filtered)
	if len(filtered) != 1 || filtered[0]["name"] != "ABCテレビ" {
		t.Errorf("channel.channel filter = %v", filtered)
	}
	// filter by serviceId (数値)
	w = do(t, h, "GET", "/api/services?serviceId=1", nil, nil)
	decodeJSON(t, w.Body.Bytes(), &filtered)
	if len(filtered) != 1 || filtered[0]["serviceId"] != float64(1) {
		t.Errorf("serviceId filter = %v", filtered)
	}

	// single
	id := core.GetServiceItemID(32721, 1)
	w = do(t, h, "GET", "/api/services/"+itoa64(id), nil, nil)
	if w.Code != 200 {
		t.Fatalf("single service status = %d", w.Code)
	}

	// logo: logoId 3 だがファイル無し -> 503
	w = do(t, h, "GET", "/api/services/"+itoa64(id)+"/logo", nil, nil)
	if w.Code != 503 {
		t.Errorf("logo status = %d, want 503", w.Code)
	}

	// logo ファイルがあれば 200 image/png
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a}
	if err := core.SaveLogoData(env.cfg.LogoDataDirPath, 32721, 3, png); err != nil {
		t.Fatalf("SaveLogoData: %v", err)
	}
	w = do(t, h, "GET", "/api/services/"+itoa64(id)+"/logo", nil, nil)
	if w.Code != 200 {
		t.Fatalf("logo status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("logo Content-Type = %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=86400" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if !strings.HasPrefix(w.Body.String(), "\x89PNG") {
		t.Errorf("logo body = %q", w.Body.String())
	}

	// logoId を持たないサービス -> 503
	id2 := core.GetServiceItemID(32723, 2)
	w = do(t, h, "GET", "/api/services/"+itoa64(id2)+"/logo", nil, nil)
	if w.Code != 503 {
		t.Errorf("logo(no logoId) status = %d, want 503", w.Code)
	}
}

func TestStreamHeadAndGet(t *testing.T) {
	tuner := newStubTuner()
	env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
	h := env.server.Handler()
	id := core.GetServiceItemID(32721, 1)

	// HEAD: チューナーを起動しない
	w := do(t, h, "HEAD", "/api/services/"+itoa64(id)+"/stream", nil, nil)
	if w.Code != 200 {
		t.Fatalf("HEAD status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "video/MP2T" {
		t.Errorf("HEAD Content-Type = %q", ct)
	}
	if w.Header().Get("X-Mirakurun-Tuner-User-ID") == "" {
		t.Errorf("HEAD X-Mirakurun-Tuner-User-ID missing")
	}
	if tuner.initCalls != 0 {
		t.Errorf("HEAD must not start tuner, initCalls = %d", tuner.initCalls)
	}

	// GET: ストリーム成功
	tuner.streamData = []byte{0x47, 0x40, 0x00, 0x10}
	w = do(t, h, "GET", "/api/services/"+itoa64(id)+"/stream", nil, map[string]string{"X-Mirakurun-Priority": "5"})
	if w.Code != 200 {
		t.Fatalf("GET status = %d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "video/MP2T" {
		t.Errorf("GET Content-Type = %q", ct)
	}
	if !strings.HasPrefix(w.Body.String(), "\x47\x40\x00\x10") {
		t.Errorf("GET body = %q", w.Body.String())
	}
	if tuner.initCalls != 1 {
		t.Fatalf("initCalls = %d", tuner.initCalls)
	}
	if tuner.lastReq.Priority != 5 {
		t.Errorf("priority = %d, want 5", tuner.lastReq.Priority)
	}
	if tuner.lastReq.DisableDecoder {
		t.Errorf("DisableDecoder must be false (Node compat)")
	}
	if !strings.HasPrefix(tuner.lastReq.ID, "127.0.0.1:") {
		t.Errorf("user id = %q", tuner.lastReq.ID)
	}
}

func TestStreamNoAvailableTuners(t *testing.T) {
	tuner := newStubTuner()
	tuner.initErr = ErrNoAvailableTuners
	env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
	h := env.server.Handler()
	id := core.GetServiceItemID(32721, 1)

	w := do(t, h, "GET", "/api/services/"+itoa64(id)+"/stream", nil, nil)
	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	want := `{"code":503,"reason":"Tuner Resource Unavailable","errors":[]}`
	if got := w.Body.String(); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestStreamOtherError(t *testing.T) {
	tuner := newStubTuner()
	tuner.initErr = errString("stream has closed before get network")
	env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
	h := env.server.Handler()
	w := do(t, h, "GET", "/api/services/"+itoa64(core.GetServiceItemID(32721, 1))+"/stream", nil, nil)
	if w.Code != 500 {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if got := w.Body.String(); got != `{"code":500,"reason":"stream has closed before get network","errors":[]}` {
		t.Errorf("body = %s", got)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestTunersAndProcessEndpoints(t *testing.T) {
	tuner := newStubTuner()
	pid := 4242
	dev := &stubDevice{idx: 0, name: "Tuner0", pid: &pid}
	tuner.devices = []*stubDevice{dev}
	env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
	h := env.server.Handler()

	w := do(t, h, "GET", "/api/tuners", nil, nil)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var devices []map[string]any
	decodeJSON(t, w.Body.Bytes(), &devices)
	if len(devices) != 1 || devices[0]["name"] != "Tuner0" {
		t.Fatalf("devices = %v", devices)
	}
	// フィールド順は index,name,types,command,pid,users,isAvailable,isRemote,isFree,isUsing,isFault
	wantPrefix := `[{"index":0,"name":"Tuner0","types":["GR"],"command":null,"pid":4242,`
	if !strings.HasPrefix(w.Body.String(), wantPrefix) {
		t.Errorf("tuners body prefix = %s", w.Body.String()[:minInt(len(w.Body.String()), 120)])
	}

	if w := do(t, h, "GET", "/api/tuners/0", nil, nil); w.Code != 200 {
		t.Errorf("tuner status = %d", w.Code)
	}
	w = do(t, h, "GET", "/api/tuners/0/process", nil, nil)
	if w.Code != 200 || w.Body.String() != `{"pid":4242}` {
		t.Errorf("process = %d %s", w.Code, w.Body.String())
	}
	w = do(t, h, "DELETE", "/api/tuners/0/process", nil, nil)
	if w.Code != 204 {
		t.Errorf("kill status = %d, want 204", w.Code)
	}
	if !dev.killed {
		t.Errorf("device was not killed")
	}

	// pid なし -> 404
	dev2 := &stubDevice{idx: 1, name: "Tuner1"}
	tuner.devices = append(tuner.devices, dev2)
	if w := do(t, h, "GET", "/api/tuners/1/process", nil, nil); w.Code != 404 {
		t.Errorf("process(no pid) = %d, want 404", w.Code)
	}
}

func TestJobsAndSchedulesEndpoints(t *testing.T) {
	tuner := newStubTuner()
	env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
	h := env.server.Handler()

	// Job スケジューラは既定ジョブ (Program.GC / EPG.Gatherer) を登録するため空ではない。
	w := do(t, h, "GET", "/api/jobs", nil, nil)
	if w.Code != 200 {
		t.Fatalf("jobs = %d", w.Code)
	}
	var jobs []map[string]any
	decodeJSON(t, w.Body.Bytes(), &jobs)
	if len(jobs) == 0 {
		t.Fatalf("jobs should contain built-in jobs: %s", w.Body.String())
	}
	if _, ok := jobs[0]["key"]; !ok {
		t.Errorf("job missing key: %v", jobs[0])
	}

	w = do(t, h, "GET", "/api/job-schedules", nil, nil)
	if w.Code != 200 {
		t.Fatalf("schedules = %d", w.Code)
	}
	var schedules []map[string]any
	decodeJSON(t, w.Body.Bytes(), &schedules)
	if len(schedules) == 0 {
		t.Fatalf("schedules should contain built-in schedules: %s", w.Body.String())
	}
	if _, ok := schedules[0]["schedule"]; !ok {
		t.Errorf("schedule missing schedule: %v", schedules[0])
	}

	// 存在しないジョブの abort / rerun -> 409
	w = do(t, h, "PUT", "/api/jobs/none/abort", nil, nil)
	if w.Code != 409 {
		t.Fatalf("abort status = %d, want 409", w.Code)
	}
	if got := w.Body.String(); got != `{"code":409,"reason":"`+busyJobReason+`","errors":[]}` {
		t.Errorf("abort body = %s", got)
	}
	if w := do(t, h, "PUT", "/api/jobs/none/rerun", nil, nil); w.Code != 409 {
		t.Errorf("rerun status = %d, want 409", w.Code)
	}

	// job-schedules/{key}/run は Node 同様に常に 202
	if w := do(t, h, "PUT", "/api/job-schedules/whatever/run", nil, nil); w.Code != 202 {
		t.Errorf("run schedule status = %d, want 202", w.Code)
	}
}

func TestStatusVersionEventsLogEndpoints(t *testing.T) {
	tuner := newStubTuner()
	env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
	env.server.opts.LatestVersion = func() (string, error) { return "9.9.9", nil }
	h := env.server.Handler()

	w := do(t, h, "GET", "/api/status", nil, nil)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("status Content-Type = %q", ct)
	}
	var st map[string]any
	decodeJSON(t, w.Body.Bytes(), &st)
	if st["version"] != "4.1.5" {
		t.Errorf("status.version = %v", st["version"])
	}

	w = do(t, h, "GET", "/api/version", nil, nil)
	if w.Code != 200 || w.Body.String() != `{"current":"4.1.5","latest":"9.9.9"}` {
		t.Errorf("version = %d %s", w.Code, w.Body.String())
	}

	// events: 起動直後から Job/JobSchedule/Service のイベントが積まれている。
	w = do(t, h, "GET", "/api/events", nil, nil)
	if w.Code != 200 {
		t.Fatalf("events = %d", w.Code)
	}
	var before []map[string]any
	decodeJSON(t, w.Body.Bytes(), &before)

	env.app.Event.Emit(apid.EventResourceProgram, apid.EventTypeCreate, apid.Program{ID: 1, NetworkID: 32721, ServiceID: 1, EventID: 1})

	w = do(t, h, "GET", "/api/events", nil, nil)
	var evs []map[string]any
	decodeJSON(t, w.Body.Bytes(), &evs)
	if len(evs) != len(before)+1 {
		t.Errorf("events count = %d, want %d", len(evs), len(before)+1)
	}
	last := evs[len(evs)-1]
	if last["resource"] != "program" || last["type"] != "create" {
		t.Errorf("last event = %v", last)
	}
	data, _ := last["data"].(map[string]any)
	if data == nil || data["id"] != float64(1) {
		t.Errorf("last event data = %v", last["data"])
	}
	if last["time"] != float64(1_700_000_000_000) {
		t.Errorf("last event time = %v", last["time"])
	}

	// log
	env.app.Log.Info("hello world")
	w = do(t, h, "GET", "/api/log", nil, nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("log = %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Body.String(), "hello world") {
		t.Errorf("log body = %q", w.Body.String())
	}
}

func TestIPTVEndpoints(t *testing.T) {
	tuner := newStubTuner()
	env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
	h := env.server.Handler()

	w := do(t, h, "GET", "http://mirakurun.local/api/iptv/discover.json", nil, nil)
	if w.Code != 200 {
		t.Fatalf("discover status = %d", w.Code)
	}
	var disc map[string]any
	decodeJSON(t, w.Body.Bytes(), &disc)
	if disc["FriendlyName"] != "Mirakurun" || disc["ModelNumber"] != "MIRAKURUN" {
		t.Errorf("discover = %v", disc)
	}
	if disc["DeviceID"] != "mirakurunlocal" {
		t.Errorf("DeviceID = %v", disc["DeviceID"])
	}
	if disc["BaseURL"] != "http://mirakurun.local/api/iptv" {
		t.Errorf("BaseURL = %v", disc["BaseURL"])
	}

	w = do(t, h, "GET", "http://mirakurun.local/api/iptv/lineup.json", nil, nil)
	var lineup []map[string]any
	decodeJSON(t, w.Body.Bytes(), &lineup)
	if len(lineup) != 2 {
		t.Fatalf("lineup = %v", lineup)
	}
	if lineup[0]["GuideNumber"] != "1.1" || lineup[0]["HD"] != float64(1) {
		t.Errorf("lineup[0] = %v", lineup[0])
	}

	w = do(t, h, "GET", "http://mirakurun.local/api/iptv/lineup_status.json", nil, nil)
	if !strings.Contains(w.Body.String(), `"Source":"Antenna"`) {
		t.Errorf("lineup_status = %s", w.Body.String())
	}

	w = do(t, h, "GET", "http://mirakurun.local/api/iptv/playlist", nil, nil)
	if ct := w.Header().Get("Content-Type"); ct != "application/x-mpegURL; charset=utf-8" {
		t.Errorf("playlist Content-Type = %q", ct)
	}
	if !strings.HasPrefix(w.Body.String(), "#EXTM3U url-tvg=\"http://mirakurun.local/api/iptv/xmltv\"") {
		t.Errorf("playlist head = %q", w.Body.String())
	}

	w = do(t, h, "GET", "http://mirakurun.local/api/iptv/xmltv", nil, nil)
	if ct := w.Header().Get("Content-Type"); ct != "text/xml; charset=utf-8" {
		t.Errorf("xmltv Content-Type = %q", ct)
	}
	if !strings.HasPrefix(w.Body.String(), "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE tv SYSTEM \"xmltv.dtd\">") {
		t.Errorf("xmltv head = %q", w.Body.String())
	}
}

func TestConfigEndpoints(t *testing.T) {
	tuner := newStubTuner()
	env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
	h := env.server.Handler()

	w := do(t, h, "GET", "/api/config/channels", nil, nil)
	if w.Code != 200 {
		t.Fatalf("config/channels = %d", w.Code)
	}
	var chans []map[string]any
	decodeJSON(t, w.Body.Bytes(), &chans)
	if len(chans) != 2 {
		t.Errorf("config channels = %v", chans)
	}

	// PUT で反映される (stubLoader 経由)
	put := `[{"name":"X","type":"GR","channel":"T1"}]`
	w = do(t, h, "PUT", "/api/config/channels", strings.NewReader(put), nil)
	if w.Code != 200 {
		t.Fatalf("PUT config/channels = %d %s", w.Code, w.Body.String())
	}
	env.loader.mu.Lock()
	got := len(env.loader.channels)
	env.loader.mu.Unlock()
	if got != 1 {
		t.Errorf("channels saved = %d, want 1", got)
	}
	if w.Body.String() != `[{"name":"X","type":"GR","channel":"T1"}]` {
		t.Errorf("PUT echo = %s", w.Body.String())
	}

	// GET server / tuners
	if w := do(t, h, "GET", "/api/config/server", nil, nil); w.Code != 200 {
		t.Errorf("config/server = %d", w.Code)
	}
	if w := do(t, h, "GET", "/api/config/tuners", nil, nil); w.Code != 200 || w.Body.String() != "[]" {
		t.Errorf("config/tuners = %d %s", w.Code, w.Body.String())
	}

	// PUT server
	if w := do(t, h, "PUT", "/api/config/server", strings.NewReader(`{"hostname":"localhost"}`), nil); w.Code != 200 {
		t.Errorf("PUT config/server = %d", w.Code)
	}

	// 不正 JSON -> 400
	if w := do(t, h, "PUT", "/api/config/channels", strings.NewReader(`not-json`), nil); w.Code != 400 {
		t.Errorf("invalid JSON = %d, want 400", w.Code)
	}
}

func TestScanStatusEndpoints(t *testing.T) {
	tuner := newStubTuner()
	env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
	h := env.server.Handler()

	w := do(t, h, "GET", "/api/config/channels/scan", nil, nil)
	if w.Code != 200 || w.Body.String() != `{"isScanning":false,"status":"not_started"}` {
		t.Errorf("scan status = %d %s", w.Code, w.Body.String())
	}
	// type 無し -> 400
	if w := do(t, h, "PUT", "/api/config/channels/scan", nil, nil); w.Code != 400 {
		t.Errorf("scan put(no type) = %d, want 400", w.Code)
	}
	// DELETE (scan なし) -> 404
	if w := do(t, h, "DELETE", "/api/config/channels/scan", nil, nil); w.Code != 404 {
		t.Errorf("scan delete = %d, want 404", w.Code)
	}
}

func TestRestartEndpoint(t *testing.T) {
	tuner := newStubTuner()
	env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
	env.server.opts.Restart = func() (int, error) { return 1234, nil }
	h := env.server.Handler()

	w := do(t, h, "PUT", "/api/restart", nil, nil)
	if w.Code != 202 || w.Body.String() != `{"_cmd_pid":1234}` {
		t.Errorf("restart = %d %s", w.Code, w.Body.String())
	}
}

func TestAccessControlAndCORS(t *testing.T) {
	tuner := newStubTuner()
	t.Run("permitted IP", func(t *testing.T) {
		env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
		srv := httptest.NewServer(env.server.Handler())
		defer srv.Close()
		resp, err := http.Get(srv.URL + "/api/status")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if resp.Header.Get("Server") == "" || !strings.HasPrefix(resp.Header.Get("Server"), "Mirakurun/") {
			t.Errorf("Server header = %q", resp.Header.Get("Server"))
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("X-Content-Type-Options = %q", resp.Header.Get("X-Content-Type-Options"))
		}
		if resp.Header.Get("X-Your-IP") != "127.0.0.1" {
			t.Errorf("X-Your-IP = %q", resp.Header.Get("X-Your-IP"))
		}
		if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("ACAO = %q", resp.Header.Get("Access-Control-Allow-Origin"))
		}
	})

	t.Run("refused IP closes connection", func(t *testing.T) {
		env := newTestEnv(t, tuner, grChannels(), sampleServices(), func(c *core.Config) {
			c.Server.AllowIPv4CidrRanges = []string{"10.0.0.0/8"}
			c.Server.AllowIPv6CidrRanges = []string{}
		})
		env.server.system = config.NewSystem(env.cfg.Server)
		srv := httptest.NewServer(env.server.Handler())
		defer srv.Close()
		_, err := http.Get(srv.URL + "/api/status")
		if err == nil {
			t.Fatalf("expected connection to be closed for refused IP")
		}
	})

	t.Run("permitted origin", func(t *testing.T) {
		env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
		h := env.server.Handler()
		w := do(t, h, "GET", "/api/status", nil, map[string]string{"Origin": "http://localhost"})
		if w.Code != 200 {
			t.Errorf("status = %d, want 200", w.Code)
		}
	})

	t.Run("refused origin -> 403", func(t *testing.T) {
		env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
		h := env.server.Handler()
		w := do(t, h, "GET", "/api/status", nil, map[string]string{"Origin": "http://evil.example.com"})
		if w.Code != 403 {
			t.Errorf("status = %d, want 403", w.Code)
		}
	})

	t.Run("preflight", func(t *testing.T) {
		env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
		h := env.server.Handler()
		w := do(t, h, "OPTIONS", "/api/status", nil, map[string]string{
			"Origin":                         "http://localhost",
			"Access-Control-Request-Method":  "GET",
			"Access-Control-Request-Headers": "x-mirakurun-priority",
		})
		if w.Code != 204 {
			t.Fatalf("preflight status = %d, want 204", w.Code)
		}
		if got := w.Header().Get("Access-Control-Allow-Methods"); got != "GET,HEAD,PUT,PATCH,POST,DELETE" {
			t.Errorf("ACAM = %q", got)
		}
		if got := w.Header().Get("Access-Control-Allow-Headers"); got != "x-mirakurun-priority" {
			t.Errorf("ACAH = %q", got)
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("ACAO = %q", w.Header().Get("Access-Control-Allow-Origin"))
		}
	})
}

func TestEventsStreamFormat(t *testing.T) {
	tuner := newStubTuner()
	env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
	srv := httptest.NewServer(env.server.Handler())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}

	buf := make([]byte, 2)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("read prefix: %v", err)
	}
	if string(buf) != "[\n" {
		t.Errorf("prefix = %q, want %q", buf, "[\n")
	}

	env.app.Event.Emit(apid.EventResourceService, apid.EventTypeUpdate, apid.Service{ID: 1, ServiceID: 1, NetworkID: 32721, Name: "x"})
	// 読み出し
	rest := make([]byte, 0, 256)
	done := make(chan struct{})
	go func() {
		b := make([]byte, 256)
		n, _ := resp.Body.Read(b)
		rest = append(rest, b[:n]...)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for event stream data")
	}
	if !strings.Contains(string(rest), "\"resource\":\"service\"") {
		t.Errorf("stream data = %q", rest)
	}
	if !strings.Contains(string(rest), "\n,\n") {
		t.Errorf("stream must terminate event with \\n,\\n: %q", rest)
	}
}

func TestConcurrencySmoke(t *testing.T) {
	tuner := newStubTuner()
	env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
	srv := httptest.NewServer(env.server.Handler())
	defer srv.Close()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				target := "/api/status"
				switch (i + j) % 4 {
				case 1:
					target = "/api/services"
				case 2:
					target = "/api/channels"
				case 3:
					target = "/api/programs"
				}
				resp, err := http.Get(srv.URL + target)
				if err != nil {
					t.Errorf("GET %s: %v", target, err)
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Errorf("GET %s: status %d", target, resp.StatusCode)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestInitAndShutdownTCP(t *testing.T) {
	tuner := newStubTuner()
	env := newTestEnv(t, tuner, grChannels(), sampleServices(), func(c *core.Config) {
		zero := 0
		c.Server.Port = &zero
		c.Server.DisableIPv6 = true
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := env.server.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !env.server.IsRunning() {
		t.Fatalf("IsRunning = false")
	}
	if err := env.server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if env.server.IsRunning() {
		t.Fatalf("IsRunning = true after shutdown")
	}
}

func TestRPCWebSocket(t *testing.T) {
	tuner := newStubTuner()
	pid := 777
	tuner.devices = []*stubDevice{{idx: 0, name: "T0", pid: &pid}}
	env := newTestEnv(t, tuner, grChannels(), sampleServices(), nil)
	srv := httptest.NewServer(env.server.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/rpc", nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	// getStatus
	writeWS(t, ctx, conn, `{"jsonrpc":"2.0","id":1,"method":"getStatus"}`)
	resp := readWS(t, ctx, conn)
	if !strings.Contains(resp, `"id":1`) || !strings.Contains(resp, `"version":"4.1.5"`) {
		t.Errorf("getStatus resp = %s", resp)
	}

	// join + notification
	writeWS(t, ctx, conn, `{"jsonrpc":"2.0","id":2,"method":"join","params":{"rooms":["events:program"]}}`)
	resp = readWS(t, ctx, conn)
	if !strings.Contains(resp, `"result":null`) {
		t.Errorf("join resp = %s", resp)
	}
	env.app.Event.Emit(apid.EventResourceProgram, apid.EventTypeCreate, apid.Program{ID: 42, NetworkID: 1, ServiceID: 1, EventID: 1})
	resp = readWS(t, ctx, conn)
	if !strings.Contains(resp, `"method":"events"`) || !strings.Contains(resp, `"array"`) {
		t.Errorf("notification = %s", resp)
	}

	// getTuners / getJobs / getJobSchedules / getServices
	for _, m := range []string{"getTuners", "getJobs", "getJobSchedules", "getServices"} {
		writeWS(t, ctx, conn, `{"jsonrpc":"2.0","id":9,"method":"`+m+`"}`)
		r := readWS(t, ctx, conn)
		if !strings.Contains(r, `"id":9`) || !strings.Contains(r, `"result"`) {
			t.Errorf("%s resp = %s", m, r)
		}
	}

	// invalid method -> -32601
	writeWS(t, ctx, conn, `{"jsonrpc":"2.0","id":3,"method":"nope"}`)
	resp = readWS(t, ctx, conn)
	if !strings.Contains(resp, `"code":-32601`) {
		t.Errorf("invalid method resp = %s", resp)
	}

	// invalid JSON -> -32700
	writeWS(t, ctx, conn, `{not json`)
	resp = readWS(t, ctx, conn)
	if !strings.Contains(resp, `"code":-32700`) {
		t.Errorf("invalid json resp = %s", resp)
	}

	// missing method -> -32601 with data
	writeWS(t, ctx, conn, `{"jsonrpc":"2.0","id":4}`)
	resp = readWS(t, ctx, conn)
	if !strings.Contains(resp, `"code":-32601`) || !strings.Contains(resp, "Method not specified") {
		t.Errorf("missing method resp = %s", resp)
	}

	// wrong version -> -32600
	writeWS(t, ctx, conn, `{"jsonrpc":"1.0","id":5,"method":"getStatus"}`)
	resp = readWS(t, ctx, conn)
	if !strings.Contains(resp, `"code":-32600`) {
		t.Errorf("wrong version resp = %s", resp)
	}

	// notification (no id) -> no response
	writeWS(t, ctx, conn, `{"jsonrpc":"2.0","method":"getStatus"}`)
	// 直後に別リクエストを送り、応答が通知のものと混ざらないことを確認
	writeWS(t, ctx, conn, `{"jsonrpc":"2.0","id":6,"method":"getStatus"}`)
	resp = readWS(t, ctx, conn)
	if !strings.Contains(resp, `"id":6`) {
		t.Errorf("expected response for id 6, got %s", resp)
	}
}

func writeWS(t *testing.T, ctx context.Context, conn *websocket.Conn, msg string) {
	t.Helper()
	if err := conn.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
		t.Fatalf("ws write: %v", err)
	}
}

func readWS(t *testing.T, ctx context.Context, conn *websocket.Conn) string {
	t.Helper()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	return string(data)
}
