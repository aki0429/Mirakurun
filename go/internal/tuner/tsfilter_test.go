package tuner

import (
	"io"
	"testing"
)

// TestTSFilterContinuityCounterDrops は CC (continuity_counter) の欠落を
// Node 版と同じ規則でドロップ計上することを検証する。
func TestTSFilterContinuityCounterDrops(t *testing.T) {
	app, _ := buildApp(t, nil, nil, nil)

	tf := NewTSFilter(app, TSFilterOptions{Output: io.Discard, ParseEIT: true})

	// PID 0x12 (EIT) の CC を意図的に飛ばす。
	// counter=-1 から開始し、前回+1 以外はドロップになる。
	ccs := []int{0, 1, 2, 5, 6, 9, 0, 1}
	// 期待ドロップ: 2->5, 6->9, 9->0 の 3 箇所
	wantDrops := int64(3)

	// payload は pointer_field=0 のみ (= セクション無し)。PUSI を立てて
	// セクションとして扱わせるが、pointer_field 直後が 0xFF のため何も解析されない。
	for _, cc := range ccs {
		pkt := makePacket(0x12, true, cc, []byte{0x00})
		if _, err := tf.Write(pkt); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	if got := tf.Drops(); got != wantDrops {
		t.Fatalf("Drops() = %d, want %d", got, wantDrops)
	}

	// 連続している場合はドロップしないことも確認する。
	tf2 := NewTSFilter(app, TSFilterOptions{Output: io.Discard, ParseEIT: true})
	for cc := 0; cc < 16; cc++ {
		if _, err := tf2.Write(makePacket(0x12, true, cc, []byte{0x00})); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if got := tf2.Drops(); got != 0 {
		t.Fatalf("continuous Drops() = %d, want 0", got)
	}
}

// TestTSFilterRelayAllPIDs はチャンネルストリーム (provideAll) が全 PID を中継することを検証する。
func TestTSFilterRelayAllPIDs(t *testing.T) {
	app, _ := buildApp(t, nil, nil, nil)
	out := newSentinelWriter(true)
	tf := NewTSFilter(app, TSFilterOptions{Output: out})

	pkts := [][]byte{
		makePacket(0x0100, false, 0, []byte{1, 2, 3, 4}),
		makePacket(0x0200, false, 0, []byte{5, 6, 7, 8}),
	}
	var raw []byte
	for _, p := range pkts {
		raw = append(raw, p...)
		if _, err := tf.Write(p); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// NULL パケット (PID 0x1FFF) は中継されない
	if _, err := tf.Write(makePacket(0x1FFF, false, 0, nil)); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := out.bytes()
	if string(got) != string(raw) {
		t.Fatalf("relayed bytes mismatch: got %d bytes, want %d bytes", len(got), len(raw))
	}
}
