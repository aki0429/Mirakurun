package aribts

import (
	"testing"
	"time"
)

// TestMJDToUnixMs は ARIB STD-B10 の例 (0xC079124500 = 1993/10/13 12:45:00 JST) を
// Node 版 getTimeFromMJD と同じく JST とみなして UNIX ミリ秒に変換できることを確認する
func TestMJDToUnixMs(t *testing.T) {
	want := time.Date(1993, 10, 13, 12, 45, 0, 0, time.FixedZone("JST", 9*3600)).UnixMilli()
	if got := MJDToUnixMs([]byte{0xC0, 0x79, 0x12, 0x45, 0x00}); got != want {
		t.Fatalf("MJDToUnixMs = %d, want %d", got, want)
	}
	// MJD 40587 (1970-01-01) 00:00:00 JST は UNIX 時刻 -9 時間になる
	if got := MJDToUnixMs([]byte{0x9E, 0x8B, 0x00, 0x00, 0x00}); got != -9*3600*1000 {
		t.Fatalf("MJDToUnixMs(epoch) = %d", got)
	}
	// BCD として不正なニブル (0xF) も Node 版と同様に単純な算術で扱う: 0xFF -> 15*10+15 = 165
	got := MJDToUnixMs([]byte{0x9E, 0x8B, 0xFF, 0xFF, 0xFF})
	if want := int64(((165-9)*3600 + 165*60 + 165) * 1000); got != want {
		t.Fatalf("MJDToUnixMs(invalid bcd) = %d, want %d", got, want)
	}
}

// TestBCD24ToMs は 24bit BCD の時分秒 (duration) をミリ秒に変換できることを確認する
func TestBCD24ToMs(t *testing.T) {
	if got := BCD24ToMs([]byte{0x01, 0x30, 0x15}); got != (3600+30*60+15)*1000 {
		t.Fatalf("BCD24ToMs = %d", got)
	}
	if got := BCD24ToMs([]byte{0xFF, 0xFF, 0xFF}); got != (165*3600+165*60+165)*1000 {
		t.Fatalf("BCD24ToMs(ffffff) = %d", got)
	}
}
