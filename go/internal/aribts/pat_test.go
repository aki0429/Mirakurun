package aribts

import (
	"bytes"
	"testing"
)

func TestDecodePAT(t *testing.T) {
	// TSID 0x7FE1, version 1, programs: 0 -> NIT PID 0x10, 0x0400 -> PMT PID 0x1F0
	sec := makeSection(0x00, []byte{0x7F, 0xE1, 0xC3, 0x00, 0x00, 0x00, 0x00, 0xE0, 0x10, 0x04, 0x00, 0xE1, 0xF0})
	pat, err := DecodePAT(sec)
	if err != nil {
		t.Fatal(err)
	}
	if pat.TableID != 0 || pat.TransportStreamID != 0x7FE1 || pat.VersionNumber != 1 || pat.CurrentNextIndicator != 1 {
		t.Fatalf("header: %+v", pat)
	}
	if len(pat.Programs) != 2 {
		t.Fatalf("programs: %+v", pat.Programs)
	}
	if p := pat.Programs[0]; p.ProgramNumber != 0 || p.NetworkPID != 0x10 || p.ProgramMapPID != 0 {
		t.Fatalf("program0: %+v", p)
	}
	if p := pat.Programs[1]; p.ProgramNumber != 0x400 || p.ProgramMapPID != 0x1F0 || p.NetworkPID != 0 {
		t.Fatalf("program1: %+v", p)
	}
	if !bytes.Equal(pat.Raw, sec) {
		t.Fatalf("raw mismatch")
	}
}

func TestDecodePATBadCRC(t *testing.T) {
	sec := makeSection(0x00, []byte{0x7F, 0xE1, 0xC3, 0x00, 0x00, 0x00, 0x00, 0xE0, 0x10})
	sec[len(sec)-1] ^= 0xFF
	if pat, err := DecodePAT(sec); err == nil || pat != nil {
		t.Fatalf("expected CRC error, got %+v", pat)
	}
}

// TestReaderNodeQuirks は Node 版 TsReader の癖 (readBytesRaw がビット位置とバイト長を比較する、
// 範囲外のビット読み取りは 0 を返す、負の長さで Buffer#slice の負インデックス規則が働く) を再現していることを確認する
func TestReaderNodeQuirks(t *testing.T) {
	r := &reader{buf: []byte{1, 2, 3}}
	r.next(8)
	if b := r.readBytes(5); len(b) != 2 || r.pos != 48 {
		t.Fatalf("readBytes(5): len=%d pos=%d", len(b), r.pos)
	}
	r = &reader{buf: []byte{1, 2, 3}}
	r.next(8)
	if b := r.readBytes(30); len(b) != 0 || r.pos != 38 {
		t.Fatalf("readBytes(30): len=%d pos=%d", len(b), r.pos)
	}
	r = &reader{buf: []byte{1, 2, 3}}
	r.next(8)
	if b := r.readBytes(-2); len(b) != 1 || b[0] != 2 || r.pos != -8 {
		t.Fatalf("readBytes(-2): %v pos=%d", b, r.pos)
	}
	// Node 版 readBits(32) は先頭 1bit と残り 31bit を「加算」する (乗算が抜けている) ため、
	// 0x80000001 は 1 + 1 = 2、0xFFFFFFFF は 1 + 0x7FFFFFFF = 2147483648 になる
	r = &reader{buf: []byte{0x80, 0, 0, 1}}
	if v := r.uimsbf(32); v != 2 {
		t.Fatalf("uimsbf(32) = %d", v)
	}
	r = &reader{buf: []byte{0xFF, 0xFF, 0xFF, 0xFF}}
	if v := r.uimsbf(32); v != 2147483648 {
		t.Fatalf("uimsbf(32) = %d", v)
	}
	r = &reader{buf: []byte{0x12, 0x34, 0x56, 0x78}}
	if v := r.uimsbf(32); v != 0x12345678 {
		t.Fatalf("uimsbf(32) = %d", v)
	}
	r = &reader{buf: []byte{0xAB}}
	if v := r.uimsbf(16); v != 0 || r.pos != 16 {
		t.Fatalf("out of range uimsbf: %d pos=%d", v, r.pos)
	}
}
