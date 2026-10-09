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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/config"
	"github.com/aki0429/Mirakurun/go/internal/core"
	"github.com/aki0429/Mirakurun/go/internal/event"
	"github.com/aki0429/Mirakurun/go/internal/job"
)

// fixtureDir はリポジトリの .hermes-development/fixtures を指す。
// go/internal/server から ../../../.hermes-development/fixtures。
func fixtureDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "..", "..", ".hermes-development", "fixtures")
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Skipf("fixtures not found at %s: %v", abs, err)
	}
	return abs
}

// loadFixtureArray は JSON ファイルを RawMessage の配列として読む (先頭の __integrity__ 付き)。
func loadFixtureArray(t *testing.T, path string) []json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(b, &arr); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return arr
}

// dropIntegrityHeader は先頭の {"__integrity__":...} を落とす。
func dropIntegrityHeader(arr []json.RawMessage) []json.RawMessage {
	if len(arr) == 0 {
		return arr
	}
	var probe map[string]json.RawMessage
	if json.Unmarshal(arr[0], &probe) == nil {
		if _, ok := probe["__integrity__"]; ok {
			return arr[1:]
		}
	}
	return arr
}

func toMap(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal element: %v", err)
	}
	return m
}

// extendedKeyOrder は要素の extended オブジェクトのキー順を返す (無ければ nil)。
func extendedKeyOrder(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	ext, ok := m["extended"]
	if !ok {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(ext))
	tok, err := dec.Token()
	if err != nil {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil
	}
	var keys []string
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return keys
		}
		keys = append(keys, kt.(string))
		var v any
		if err := dec.Decode(&v); err != nil {
			return keys
		}
	}
	return keys
}

// newFixtureEnv は fixture の channels.yml + services.json + programs.json で App を組み立てる。
func newFixtureEnv(t *testing.T) (*testEnv, string, string) {
	t.Helper()
	fx := fixtureDir(t)
	channelsPath := filepath.Join(fx, "config", "mirakurun", "channels.yml")
	servicesPath := filepath.Join(fx, "db", "services.json")
	programsPath := filepath.Join(fx, "db", "programs.json")

	loader := &config.Loader{ChannelsConfigPath: channelsPath}
	channels, err := loader.LoadChannels()
	if err != nil {
		t.Fatalf("LoadChannels: %v", err)
	}
	integrity, err := config.ChannelsIntegrityFromFile(channelsPath)
	if err != nil {
		t.Fatalf("ChannelsIntegrityFromFile: %v", err)
	}

	// DB はコピーして使う (Load が save を呼んでも fixture を壊さない)。
	dir := t.TempDir()
	svcCopy := filepath.Join(dir, "services.json")
	prgCopy := filepath.Join(dir, "programs.json")
	copyFile(t, servicesPath, svcCopy)
	copyFile(t, programsPath, prgCopy)

	tuner := newStubTuner()
	srvCfg := &apid.ConfigServer{
		Hostname:            "localhost",
		AllowIPv4CidrRanges: []string{"127.0.0.0/8"},
		AllowIPv6CidrRanges: []string{"::1/128"},
	}
	cfg := &core.Config{
		Server:            srvCfg,
		Channels:          channels,
		ChannelsIntegrity: integrity,
		ServicesDBPath:    svcCopy,
		ProgramsDBPath:    prgCopy,
		LogoDataDirPath:   filepath.Join(dir, "logo"),
	}
	// fixture の番組が落ちないよう、番組より前の時刻に固定する。
	clock := job.NewFakeClock(time.Unix(1_700_000_000, 0))
	ev := event.New(event.Options{Now: func() time.Time { return clock.Now() }})
	app := core.New(core.AppOptions{Config: cfg, Tuner: tuner, Clock: clock, Log: quietLogger(), Event: ev, Version: "4.1.5"})
	if err := app.Load(); err != nil {
		t.Fatalf("app.Load: %v", err)
	}
	sl := &stubLoader{server: srvCfg, channels: cfg.Channels}
	s := New(Options{App: app, Tuner: tuner, Loader: sl, System: config.NewSystem(srvCfg), Version: "4.1.5", Logger: quietLogger(), TestMode: true, Now: func() time.Time { return clock.Now() }})
	return &testEnv{server: s, app: app, tuner: tuner, loader: sl, clock: clock, cfg: cfg}, servicesPath, programsPath
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open %s: %v", src, err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("create %s: %v", dst, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatalf("copy: %v", err)
	}
}

// TestDifferentialServices は GET /api/services が Node 生成の services.json と
// フィールド単位で一致することを検証する (hasLogoData だけは API 側の付加情報)。
func TestDifferentialServices(t *testing.T) {
	env, servicesPath, _ := newFixtureEnv(t)
	h := env.server.Handler()

	if env.app.Service.Len() == 0 {
		t.Fatalf("no services loaded from fixture")
	}

	w := do(t, h, "GET", "/api/services", nil, nil)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var apiArr []json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &apiArr); err != nil {
		t.Fatalf("api services unmarshal: %v", err)
	}
	fileArr := dropIntegrityHeader(loadFixtureArray(t, servicesPath))

	if len(apiArr) != len(fileArr) {
		t.Errorf("service count: api=%d file=%d", len(apiArr), len(fileArr))
	}

	// id -> file element
	fileByID := map[any]map[string]any{}
	fileOrder := []any{}
	for _, raw := range fileArr {
		m := toMap(t, raw)
		fileByID[m["id"]] = m
		fileOrder = append(fileOrder, m["id"])
	}

	matched := 0
	mismatches := []string{}
	apiOrder := []any{}
	hasLogoTypeOK := true
	for _, raw := range apiArr {
		m := toMap(t, raw)
		apiOrder = append(apiOrder, m["id"])

		if _, ok := m["hasLogoData"]; !ok {
			hasLogoTypeOK = false
			mismatches = append(mismatches, fmt.Sprintf("id %v: hasLogoData missing", m["id"]))
		} else if _, isBool := m["hasLogoData"].(bool); !isBool {
			hasLogoTypeOK = false
			mismatches = append(mismatches, fmt.Sprintf("id %v: hasLogoData is not bool", m["id"]))
		}
		delete(m, "hasLogoData")

		fm, ok := fileByID[m["id"]]
		if !ok {
			mismatches = append(mismatches, fmt.Sprintf("id %v: not present in fixture", m["id"]))
			continue
		}
		if !reflect.DeepEqual(m, fm) {
			mismatches = append(mismatches, fmt.Sprintf("id %v:\n  api = %s\n  fixture = %s", m["id"], mustJSON(m), mustJSON(fm)))
			continue
		}
		matched++
	}

	orderOK := fmt.Sprint(apiOrder) == fmt.Sprint(fileOrder)
	t.Logf("differential(services): matched=%d/%d (fixture=%d) hasLogoData_ok=%v order_match=%v",
		matched, len(apiArr), len(fileArr), hasLogoTypeOK, orderOK)

	if len(mismatches) > 0 {
		for _, m := range mismatches {
			t.Errorf("service mismatch: %s", m)
		}
	}
	if matched != len(fileArr) {
		t.Errorf("differential(services): matched=%d, want %d", matched, len(fileArr))
	}
	if !orderOK {
		t.Logf("NOTE: /api/services order (getOrder) differs from services.json insertion order; that is expected (Node sorts by getOrder).")
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestDifferentialPrograms は GET /api/programs?networkId=N が programs.json のサブセットと
// フィールド単位で一致することを検証する (extended のキー順も比較)。
func TestDifferentialPrograms(t *testing.T) {
	env, _, programsPath := newFixtureEnv(t)
	h := env.server.Handler()

	if env.app.Program.Len() == 0 {
		t.Fatalf("no programs loaded from fixture")
	}

	fileArr := dropIntegrityHeader(loadFixtureArray(t, programsPath))

	// networkId ごとの件数を数え、最も少ない (>0) を選ぶ (bounded query)。
	countByNID := map[int]int{}
	nidOf := func(raw json.RawMessage) int {
		var m struct {
			NetworkID int `json:"networkId"`
		}
		_ = json.Unmarshal(raw, &m)
		return m.NetworkID
	}
	for _, raw := range fileArr {
		countByNID[nidOf(raw)]++
	}
	nid := 0
	best := 1 << 30
	for k, v := range countByNID {
		if v > 0 && v < best {
			best = v
			nid = k
		}
	}
	if nid == 0 {
		t.Fatalf("no networkId found")
	}
	t.Logf("differential(programs): networkId=%d fixture_count=%d (total nids=%d)", nid, best, len(countByNID))

	w := do(t, h, "GET", "/api/programs?networkId="+itoa(nid), nil, nil)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var apiArr []json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &apiArr); err != nil {
		t.Fatalf("api programs unmarshal: %v", err)
	}

	// fixture 側の該当サブセット (順序は挿入順 = programs.json 順)。
	var fileSubset []json.RawMessage
	for _, raw := range fileArr {
		if nidOf(raw) == nid {
			fileSubset = append(fileSubset, raw)
		}
	}

	if len(apiArr) != len(fileSubset) {
		t.Errorf("program count for networkId %d: api=%d fixture=%d", nid, len(apiArr), len(fileSubset))
	}

	total := len(fileSubset)
	matched := 0
	mismatches := []string{}
	extChecked := 0
	extOrderMismatch := 0
	for i := 0; i < len(apiArr) && i < len(fileSubset); i++ {
		am := toMap(t, apiArr[i])
		fm := toMap(t, fileSubset[i])
		if !reflect.DeepEqual(am, fm) {
			mismatches = append(mismatches, fmt.Sprintf("index %d (id %v):\n  api = %s\n  fixture = %s", i, am["id"], mustJSON(am), mustJSON(fm)))
			continue
		}
		matched++

		ak := extendedKeyOrder(t, apiArr[i])
		fk := extendedKeyOrder(t, fileSubset[i])
		if ak != nil || fk != nil {
			extChecked++
			if fmt.Sprint(ak) != fmt.Sprint(fk) {
				extOrderMismatch++
				mismatches = append(mismatches, fmt.Sprintf("index %d extended key order: api=%v fixture=%v", i, ak, fk))
			}
		}
	}

	t.Logf("differential(programs): matched=%d/%d (networkId=%d) extended_checked=%d ext_order_mismatch=%d",
		matched, total, nid, extChecked, extOrderMismatch)

	for _, m := range mismatches {
		t.Errorf("program mismatch: %s", m)
	}
	if matched != total {
		t.Errorf("differential(programs): matched=%d, want %d", matched, total)
	}
}

// TestDifferentialFieldOrder は /api/programs の 1 要素のフィールド順が apid.Program の定義順
// (id,eventId,serviceId,networkId,startAt,duration,isFree,name,description,genres,...) であることを確認する。
func TestDifferentialFieldOrder(t *testing.T) {
	_, _, programsPath := newFixtureEnv(t)
	arr := dropIntegrityHeader(loadFixtureArray(t, programsPath))
	if len(arr) == 0 {
		t.Skip("no programs")
	}
	keys := objectKeyOrder(t, arr[0])
	wantPrefix := []string{"id", "eventId", "serviceId", "networkId", "startAt", "duration", "isFree", "name", "description"}
	if len(keys) < len(wantPrefix) {
		t.Fatalf("keys = %v", keys)
	}
	for i, k := range wantPrefix {
		if keys[i] != k {
			t.Errorf("field order[%d] = %q, want %q (keys=%v)", i, keys[i], k, keys)
		}
	}
	// fixture 側の並びは Node 独自 (genres が後ろ) なので、Go 側は apid の順であることだけ確認する。
	// 実際のレスポンス順も確認する。
	env, _, _ := newFixtureEnv(t)
	h := env.server.Handler()
	w := do(t, h, "GET", "/api/programs", nil, nil)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var apiArr []json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &apiArr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(apiArr) == 0 {
		t.Skip("no programs in API")
	}
	apiKeys := objectKeyOrder(t, apiArr[0])
	for i, k := range wantPrefix {
		if i >= len(apiKeys) || apiKeys[i] != k {
			t.Errorf("API field order[%d] = %q, want %q (keys=%v)", i, apiKeys[i], k, apiKeys)
			break
		}
	}
	t.Logf("API first program key order: %v", apiKeys)
}

func objectKeyOrder(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		t.Fatalf("not object: %s", raw[:minInt(len(raw), 40)])
	}
	var keys []string
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			t.Fatalf("key token: %v", err)
		}
		keys = append(keys, kt.(string))
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("value: %v", err)
		}
	}
	return keys
}

// sortedStringsForDebug はデバッグ用。
func sortedStringsForDebug(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

var _ = sortedStringsForDebug
var _ = strings.TrimSpace
var _ = httptest.NewRecorder
var _ = http.MethodGet
