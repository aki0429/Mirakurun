package tuner

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
)

// TestTunerDeviceRelayViaFakeCommand は偽チューナーコマンド (faketuner cat) を用いて
// TunerDevice のプロセス起動 → stdout 読み取り → TSFilter 中継までを通しで検証する。
func TestTunerDeviceRelayViaFakeCommand(t *testing.T) {
	chanCfg := []apid.ConfigChannelsItem{{Name: "T13", Type: apid.ChannelTypeGR, Channel: "T13"}}

	// コマンド文字列を確定するためファイルを作ってから Tuner を組み立てる。
	dir := t.TempDir()
	var raw []byte
	for i := 0; i < 10; i++ {
		p := makePacket(0x0100, false, i, []byte{byte(i), 1, 2, 3})
		raw = append(raw, p...)
	}
	file := filepath.Join(dir, "in.ts")
	if err := os.WriteFile(file, raw, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	tuners := []apid.ConfigTunersItem{{
		Name:    "fake",
		Types:   []apid.ChannelType{apid.ChannelTypeGR},
		Command: fakeCommand("cat", file, "30000"),
	}}

	app, _ := buildApp(t, chanCfg, nil, tuners)
	tun := newTuner(app)
	defer tun.Shutdown()

	ch := mustChannel(t, app, apid.ChannelTypeGR, "T13")
	out := newSentinelWriter(true)
	tf, err := tun.InitChannelStream(ch, UserRequest{ID: "relay", Priority: 0, DisableDecoder: true}, out)
	if err != nil {
		t.Fatalf("InitChannelStream: %v", err)
	}
	if !out.waitSentinel(30 * time.Second) {
		t.Fatalf("sentinel not received (device did not relay input)")
	}
	_ = tf.Close()

	got := out.bytes()
	if len(got) < len(raw) || string(got[:len(raw)]) != string(raw) {
		t.Fatalf("relayed stream mismatch: got %d bytes, want prefix %d bytes", len(got), len(raw))
	}
}

// TestTunerPreemptionAndRefusal は 1 台のチューナーを 2 つのストリームが異なる優先度で要求したときの
// Node 互換挙動 (高優先度は乗っ取り、同等以下は拒否) を検証する。
func TestTunerPreemptionAndRefusal(t *testing.T) {
	chanCfg := []apid.ConfigChannelsItem{
		{Name: "T13", Type: apid.ChannelTypeGR, Channel: "T13"},
		{Name: "T14", Type: apid.ChannelTypeGR, Channel: "T14"},
	}
	tuners := []apid.ConfigTunersItem{{
		Name:    "fake",
		Types:   []apid.ChannelType{apid.ChannelTypeGR},
		Command: fakeCommand("hold", "30000"),
	}}

	app, _ := buildApp(t, chanCfg, nil, tuners)
	tun := newTuner(app)
	defer tun.Shutdown()

	chA := mustChannel(t, app, apid.ChannelTypeGR, "T13")
	chB := mustChannel(t, app, apid.ChannelTypeGR, "T14")

	// 優先度 0 の利用者 A がチューナーを占有する。
	tf1, err := tun.initTS(
		UserRequest{ID: "A", Priority: 0, DisableDecoder: true},
		&apid.StreamSetting{Channel: &chanCfg[0]}, chA, io.Discard,
	)
	if err != nil {
		t.Fatalf("initTS A: %v", err)
	}

	dev := tun.Get(0)
	if dev == nil || !dev.IsUsing() {
		t.Fatalf("device should be in use by user A")
	}

	devices := tun.devicesByType(apid.ChannelTypeGR)

	// 同等優先度 (0) は拒否される (Node: 空きが無ければ "no available tuners")。
	if got := tun.pickTunerDevice(devices, chB, 0); got != nil {
		t.Fatalf("equal priority should be refused, got device #%d", got.Index())
	}

	// 高優先度 (1) は乗っ取り可能。
	if got := tun.pickTunerDevice(devices, chB, 1); got == nil {
		t.Fatalf("higher priority should be able to take over")
	}

	// 高優先度の利用者 B が実際に乗っ取ると、A のストリームは閉じられる。
	tf2, err := tun.initTS(
		UserRequest{ID: "B", Priority: 1, DisableDecoder: true},
		&apid.StreamSetting{Channel: &chanCfg[1]}, chB, io.Discard,
	)
	if err != nil {
		t.Fatalf("initTS B (takeover): %v", err)
	}
	defer func() { _ = tf2.Close() }()

	deadline := time.Now().Add(5 * time.Second)
	for !tf1.Closed() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !tf1.Closed() {
		t.Fatalf("old stream (user A) was not closed on preemption")
	}
	if dev.Channel() != chB {
		t.Fatalf("device channel not switched to user B's channel")
	}
}
