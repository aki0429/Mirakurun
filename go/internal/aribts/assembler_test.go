package aribts

import (
	"bytes"
	"testing"
)

// tsPacket はテスト用の 188 バイト TS パケットを組み立てる。
// af が nil でなければアダプテーションフィールド (長さバイトを除く中身) を付与し、payload の残りは 0xFF で埋める。
type tsPacketSpec struct {
	pid        uint16
	pusi       bool
	tei        bool
	scrambling byte
	cc         byte
	af         []byte // nil: AF なし / 非 nil: AF あり (中身)
	noPayload  bool
	payload    []byte
}

func buildPacket(s tsPacketSpec) []byte {
	p := bytes.Repeat([]byte{0xFF}, 188)
	p[0] = 0x47
	p[1] = byte(s.pid>>8) & 0x1F
	if s.tei {
		p[1] |= 0x80
	}
	if s.pusi {
		p[1] |= 0x40
	}
	p[2] = byte(s.pid)
	afc := byte(0x01)
	if s.af != nil {
		afc = 0x03
		if s.noPayload {
			afc = 0x02
		}
	}
	p[3] = s.scrambling<<6 | afc<<4 | s.cc&0x0F
	off := 4
	if s.af != nil {
		p[4] = byte(len(s.af))
		copy(p[5:], s.af)
		off = 5 + len(s.af)
	}
	copy(p[off:], s.payload)
	return p
}

// makeSection は table_id とボディから CRC 付きの long-form セクションを作る
func makeSection(tableID byte, body []byte) []byte {
	l := len(body) + 4
	s := []byte{tableID, 0xB0 | byte(l>>8)&0x0F, byte(l)}
	s = append(s, body...)
	c := CRC32(s)
	return append(s, byte(c>>24), byte(c>>16), byte(c>>8), byte(c))
}

func seqBody(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed + byte(i)
	}
	return b
}

func TestAssemblerSinglePacketSection(t *testing.T) {
	a := NewSectionAssembler()
	sec := makeSection(0x00, []byte{0x7F, 0xE1, 0xC1, 0, 0, 0, 0, 0xE0, 0x10})
	pkt := buildPacket(tsPacketSpec{pid: 0, pusi: true, payload: append([]byte{0}, sec...)})
	pid, secs := a.Push(pkt)
	if pid != 0 || len(secs) != 1 || !bytes.Equal(secs[0], sec) {
		t.Fatalf("pid=%d secs=%x", pid, secs)
	}
	// 返されたセクションは入力パケットのコピーでなければならない
	pkt[6] = 0x00
	if !bytes.Equal(secs[0], sec) {
		t.Fatalf("returned section aliases input packet")
	}
}

func TestAssemblerMultiPacketSection(t *testing.T) {
	a := NewSectionAssembler()
	sec := makeSection(0x4E, seqBody(400, 1))
	p1 := buildPacket(tsPacketSpec{pid: 0x12, pusi: true, cc: 3, payload: append([]byte{0}, sec[:183]...)})
	p2 := buildPacket(tsPacketSpec{pid: 0x12, cc: 4, payload: sec[183 : 183+184]})
	p3 := buildPacket(tsPacketSpec{pid: 0x12, cc: 5, payload: sec[183+184:]})
	for i, p := range [][]byte{p1, p2} {
		if _, s := a.Push(p); len(s) != 0 {
			t.Fatalf("packet %d: unexpected sections %d", i, len(s))
		}
	}
	pid, secs := a.Push(p3)
	if pid != 0x12 || len(secs) != 1 || !bytes.Equal(secs[0], sec) {
		t.Fatalf("pid=%d n=%d", pid, len(secs))
	}
}

func TestAssemblerMultipleSectionsAndStuffing(t *testing.T) {
	a := NewSectionAssembler()
	s1 := makeSection(0x42, seqBody(20, 0x10))
	s2 := makeSection(0x46, seqBody(30, 0x20))
	payload := append([]byte{0}, s1...)
	payload = append(payload, s2...)
	// 0xFF (stuffing) の後ろにあるものは無視される
	payload = append(payload, 0xFF)
	payload = append(payload, makeSection(0x42, seqBody(5, 0))...)
	_, secs := a.Push(buildPacket(tsPacketSpec{pid: 0x11, pusi: true, payload: payload}))
	if len(secs) != 2 || !bytes.Equal(secs[0], s1) || !bytes.Equal(secs[1], s2) {
		t.Fatalf("got %d sections", len(secs))
	}
}

func TestAssemblerPointerFieldCompletesPrevious(t *testing.T) {
	a := NewSectionAssembler()
	s1 := makeSection(0x4E, seqBody(200, 3)) // 207 bytes
	s2 := makeSection(0x4F, seqBody(10, 7))
	a.Push(buildPacket(tsPacketSpec{pid: 0x12, pusi: true, cc: 0, payload: append([]byte{0}, s1[:183]...)}))
	rest := s1[183:]
	payload := append([]byte{byte(len(rest))}, rest...)
	payload = append(payload, s2...)
	_, secs := a.Push(buildPacket(tsPacketSpec{pid: 0x12, pusi: true, cc: 1, payload: payload}))
	if len(secs) != 2 || !bytes.Equal(secs[0], s1) || !bytes.Equal(secs[1], s2) {
		t.Fatalf("got %d sections", len(secs))
	}
}

func TestAssemblerPointerFieldMismatchDropsPrevious(t *testing.T) {
	a := NewSectionAssembler()
	s1 := makeSection(0x4E, seqBody(200, 3))
	s2 := makeSection(0x4F, seqBody(10, 7))
	a.Push(buildPacket(tsPacketSpec{pid: 0x12, pusi: true, cc: 0, payload: append([]byte{0}, s1[:183]...)}))
	// pointer_field が残りバイト数と一致しない場合は前のセクションを破棄し、後続のセクションのみ返す
	payload := append([]byte{3, 0xAA, 0xBB, 0xCC}, s2...)
	_, secs := a.Push(buildPacket(tsPacketSpec{pid: 0x12, pusi: true, cc: 1, payload: payload}))
	if len(secs) != 1 || !bytes.Equal(secs[0], s2) {
		t.Fatalf("got %d sections", len(secs))
	}
}

func TestAssemblerCCDropResetsBuffer(t *testing.T) {
	a := NewSectionAssembler()
	sec := makeSection(0x4E, seqBody(300, 1))
	a.Push(buildPacket(tsPacketSpec{pid: 0x12, pusi: true, cc: 0, payload: append([]byte{0}, sec[:183]...)}))
	// CC が飛んだ (0 -> 2) 場合は組み立て中のセクションを破棄する
	if _, secs := a.Push(buildPacket(tsPacketSpec{pid: 0x12, cc: 2, payload: sec[183:]})); len(secs) != 0 {
		t.Fatalf("section must be dropped after CC discontinuity")
	}
	if a.Drops(0x12) != 1 {
		t.Fatalf("drops = %d", a.Drops(0x12))
	}
}

func TestAssemblerDuplicateCC(t *testing.T) {
	// Node 版は 1 回目の重複パケットをドロップ扱いせず、ペイロードもそのまま処理する (癖の再現)
	a := NewSectionAssembler()
	sec := makeSection(0x4E, seqBody(300, 1))
	a.Push(buildPacket(tsPacketSpec{pid: 0x12, pusi: true, cc: 0, payload: append([]byte{0}, sec[:183]...)}))
	dup := buildPacket(tsPacketSpec{pid: 0x12, cc: 0, payload: sec[183:]})
	_, secs := a.Push(dup)
	if len(secs) != 1 || !bytes.Equal(secs[0], sec) || a.Drops(0x12) != 0 {
		t.Fatalf("first duplicate: n=%d drops=%d", len(secs), a.Drops(0x12))
	}
	// 2 回連続の重複はドロップとして扱う
	a.Push(buildPacket(tsPacketSpec{pid: 0x12, cc: 0, payload: sec[183:]}))
	if a.Drops(0x12) != 1 {
		t.Fatalf("second duplicate: drops=%d", a.Drops(0x12))
	}
}

func TestAssemblerDiscontinuityIndicator(t *testing.T) {
	a := NewSectionAssembler()
	sec := makeSection(0x4E, seqBody(300, 1))
	a.Push(buildPacket(tsPacketSpec{pid: 0x12, pusi: true, cc: 0, payload: append([]byte{0}, sec[:183]...)}))
	// discontinuity_indicator が立っていれば CC の不連続をドロップとみなさない
	_, secs := a.Push(buildPacket(tsPacketSpec{pid: 0x12, cc: 9, af: []byte{0x80}, payload: sec[183:]}))
	if a.Drops(0x12) != 0 {
		t.Fatalf("drops=%d", a.Drops(0x12))
	}
	// AF 分ペイロードが減っても残り 124 バイトは収まるので、セクションは完成する
	if len(secs) != 1 || !bytes.Equal(secs[0], sec) {
		t.Fatalf("got %d sections", len(secs))
	}
}

func TestAssemblerTransportErrorAndScrambled(t *testing.T) {
	a := NewSectionAssembler()
	sec := makeSection(0x00, seqBody(9, 0))
	if _, secs := a.Push(buildPacket(tsPacketSpec{pid: 0, pusi: true, tei: true, payload: append([]byte{0}, sec...)})); secs != nil {
		t.Fatalf("transport error packet must be skipped")
	}
	if _, secs := a.Push(buildPacket(tsPacketSpec{pid: 0, pusi: true, scrambling: 3, cc: 0, payload: append([]byte{0}, sec...)})); secs != nil {
		t.Fatalf("scrambled packet must be skipped")
	}
	// スクランブルパケットでも CC は更新されているので、次の CC=1 はドロップにならない
	if _, secs := a.Push(buildPacket(tsPacketSpec{pid: 0, pusi: true, cc: 1, payload: append([]byte{0}, sec...)})); len(secs) != 1 {
		t.Fatalf("expected section after scrambled packet")
	}
	if a.Drops(0) != 0 {
		t.Fatalf("drops=%d", a.Drops(0))
	}
}

func TestAssemblerPESAndNoAllocation(t *testing.T) {
	a := NewSectionAssembler()
	pes := buildPacket(tsPacketSpec{pid: 0x100, pusi: true, cc: 0, payload: []byte{0, 0, 1, 0xE0}})
	if _, secs := a.Push(pes); secs != nil {
		t.Fatalf("PES must not yield sections")
	}
	cont := buildPacket(tsPacketSpec{pid: 0x100, cc: 1, payload: makeSection(0x42, seqBody(5, 0))})
	cc := byte(1)
	allocs := testing.AllocsPerRun(1000, func() {
		cont[3] = cont[3]&0xF0 | cc&0x0F
		cc++
		if _, secs := a.Push(cont); secs != nil {
			t.Fatalf("PES continuation must not yield sections")
		}
	})
	if allocs != 0 {
		t.Fatalf("Push allocated %.1f times per PES packet", allocs)
	}
	if a.Drops(0x100) != 0 {
		t.Fatalf("drops=%d", a.Drops(0x100))
	}
}

func TestAssemblerAdaptationOnlyAndShortData(t *testing.T) {
	a := NewSectionAssembler()
	// ペイロードなし (AF のみ) は何もしない
	if _, secs := a.Push(buildPacket(tsPacketSpec{pid: 0x20, af: bytes.Repeat([]byte{0}, 183), noPayload: true})); secs != nil {
		t.Fatalf("unexpected")
	}
	// AF が 183 バイトでペイロードが空の PUSI パケットでもパニックしない
	if _, secs := a.Push(buildPacket(tsPacketSpec{pid: 0x20, pusi: true, cc: 0, af: bytes.Repeat([]byte{0}, 183)})); secs != nil {
		t.Fatalf("unexpected")
	}
	// AF 長が異常に大きい場合もパニックしない
	p := buildPacket(tsPacketSpec{pid: 0x20, pusi: true, cc: 1, af: []byte{0}})
	p[4] = 250
	a.Push(p)
}
