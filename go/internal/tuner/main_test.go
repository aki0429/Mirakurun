package tuner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/core"
	"github.com/aki0429/Mirakurun/go/internal/db"
	"github.com/aki0429/Mirakurun/go/internal/mlog"
)

// fakeTunerPath はテスト用チューナーコマンド (testdata/faketuner) のビルド済みパス。
var fakeTunerPath string

// fixturesDir は .hermes-development/fixtures の絶対パス。
var fixturesDir string

// sentinelMagic は faketuner が入力消費完了時に出力する既知バイト列 (PID 0x1FFE)。
var sentinelMagic = []byte{0x47, 0x1F, 0xFE, 0x10, 0xDE, 0xAD, 0xBE, 0xEF}

// testIntegrity はテスト用の channels/services 整合性値。
const testIntegrity = "tuner-oracle-test"

// TestMain は faketuner をビルドしてからテストを実行する。
func TestMain(m *testing.M) {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "getwd failed:", err)
		os.Exit(1)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", "..", ".."))
	fixturesDir = filepath.Join(repoRoot, ".hermes-development", "fixtures")

	scratch := os.Getenv("GOTMPDIR")
	if scratch == "" {
		scratch = os.TempDir()
	}
	exe := filepath.Join(scratch, "faketuner.exe")
	if _, statErr := os.Stat(exe); statErr != nil {
		cmd := exec.Command("go", "build", "-o", exe, "./testdata/faketuner")
		cmd.Dir = wd
		if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
			fmt.Fprintf(os.Stderr, "failed to build faketuner: %v\n%s\n", buildErr, out)
			os.Exit(1)
		}
	}
	fakeTunerPath = exe

	os.Exit(m.Run())
}

// fakeCommand は faketuner のサブコマンド文字列を組み立てる。
func fakeCommand(action string, args ...string) string {
	parts := []string{`"` + fakeTunerPath + `"`, action}
	parts = append(parts, args...)
	cmd := ""
	for i, p := range parts {
		if i != 0 {
			cmd += " "
		}
		cmd += p
	}
	return cmd
}

// sentinelWriter は出力を監視して sentinelMagic を検出したら done を閉じる io.Writer。
type sentinelWriter struct {
	mu         sync.Mutex
	captureAll bool
	buf        bytes.Buffer
	tail       []byte
	done       chan struct{}
	closed     bool
}

func newSentinelWriter(captureAll bool) *sentinelWriter {
	return &sentinelWriter{captureAll: captureAll, done: make(chan struct{})}
}

func (w *sentinelWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.captureAll {
		w.buf.Write(p)
	}
	w.tail = append(w.tail, p...)
	if len(w.tail) > 512 {
		w.tail = append([]byte(nil), w.tail[len(w.tail)-512:]...)
	}
	if !w.closed && bytes.Contains(w.tail, sentinelMagic) {
		w.closed = true
		close(w.done)
	}
	return len(p), nil
}

// waitSentinel は sentinel を待つ。時間切れなら false。
func (w *sentinelWriter) waitSentinel(d time.Duration) bool {
	select {
	case <-w.done:
		return true
	case <-time.After(d):
		return false
	}
}

func (w *sentinelWriter) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf.Bytes()...)
}

// quietLogger は出力を捨てるロガーを返す。
func quietLogger() *mlog.Logger {
	return mlog.New(mlog.Options{Stdout: io.Discard, Stderr: io.Discard})
}

// buildApp はテスト用の core.App を構築する (services/programs は一時 DB に置く)。
func buildApp(t *testing.T, channels []apid.ConfigChannelsItem, services []apid.Service, tuners []apid.ConfigTunersItem) (*core.App, string) {
	t.Helper()
	dir := t.TempDir()
	svcPath := filepath.Join(dir, "services.json")
	progPath := filepath.Join(dir, "programs.json")
	if err := db.SaveServices(svcPath, services, testIntegrity); err != nil {
		t.Fatalf("save services: %v", err)
	}
	if err := db.SavePrograms(progPath, nil, testIntegrity); err != nil {
		t.Fatalf("save programs: %v", err)
	}
	cfg := &core.Config{
		Server:            &apid.ConfigServer{},
		Channels:          channels,
		Tuners:            tuners,
		ChannelsIntegrity: testIntegrity,
		ServicesDBPath:    svcPath,
		ProgramsDBPath:    progPath,
		LogoDataDirPath:   filepath.Join(dir, "logo"),
	}
	app := core.New(core.AppOptions{Config: cfg, Tuner: anyTuner{}, Log: quietLogger()})
	if err := app.Load(); err != nil {
		t.Fatalf("app load: %v", err)
	}
	return app, dir
}

// anyTuner は ChannelStore.load の TypeExists 検証を通すための起動前スタブ。
// core.New は Tuner が nil だと全チャンネルを読み飛ばすため、テストでは全種別を扱えると報告させ、
// 直後に newTuner で本物の Tuner に差し替える (本番の server も同様の二段構えが必要)。
type anyTuner struct{}

func (anyTuner) TypeExists(apid.ChannelType) bool { return true }
func (anyTuner) ReadyForJob(context.Context, *core.ChannelItem) bool {
	return false
}
func (anyTuner) GetServices(context.Context, *core.ChannelItem) ([]apid.Service, error) {
	return nil, nil
}
func (anyTuner) GetEPG(context.Context, *core.ChannelItem, time.Duration) error { return nil }

// newTuner は本物の Tuner を生成し、App へ差し替え登録する。
func newTuner(app *core.App) *Tuner {
	tun := New(app)
	app.Tuner = tun
	return tun
}

// mustChannel は Channel を取得する (無ければ Fatal)。
func mustChannel(t *testing.T, app *core.App, typ apid.ChannelType, ch string) *core.ChannelItem {
	t.Helper()
	item := app.Channel.Get(typ, ch)
	if item == nil {
		t.Fatalf("channel not found: %s/%s", typ, ch)
	}
	return item
}

// makePacket は 188 バイトの TS パケットを生成する。
func makePacket(pid int, pusi bool, cc int, payload []byte) []byte {
	p := make([]byte, packetSize)
	for i := range p {
		p[i] = 0xFF
	}
	p[0] = 0x47
	b1 := byte(pid>>8) & 0x1F
	if pusi {
		b1 |= 0x40
	}
	p[1] = b1
	p[2] = byte(pid & 0xFF)
	p[3] = 0x10 | byte(cc&0x0F)
	copy(p[4:], payload)
	return p
}

// loadFixtureServiceIDs はフィクスチャ services.json から指定ネットワークの serviceId を返す。
func loadFixtureServiceIDs(t *testing.T, path string, networkID int) []int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var ids []int
	for _, it := range arr {
		var svc apid.Service
		if err := json.Unmarshal(it, &svc); err != nil {
			continue
		}
		if svc.NetworkID == networkID && svc.ServiceID != 0 {
			ids = append(ids, svc.ServiceID)
		}
	}
	return ids
}

// nodeProgram は Node 版 programs.json の 1 レコード。
type nodeProgram struct {
	NetworkID   int     `json:"networkId"`
	ServiceID   int     `json:"serviceId"`
	EventID     int     `json:"eventId"`
	StartAt     int64   `json:"startAt"`
	Duration    int64   `json:"duration"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// loadNodePrograms は Node 版 programs.json を (networkId:serviceId:eventId) で索引化する。
func loadNodePrograms(t *testing.T, path string) map[string]nodeProgram {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := make(map[string]nodeProgram, len(arr))
	for _, it := range arr {
		var p nodeProgram
		if err := json.Unmarshal(it, &p); err != nil {
			continue
		}
		out[programKey(p.NetworkID, p.ServiceID, p.EventID)] = p
	}
	return out
}

func programKey(networkID, serviceID, eventID int) string {
	return fmt.Sprintf("%d:%d:%d", networkID, serviceID, eventID)
}

func strEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func strVal(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}
