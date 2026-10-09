package aribts

import "testing"

// TestCRC32KnownVector は MPEG-2 CRC32 (多項式 0x04C11DB7, 初期値 0xFFFFFFFF, 反転なし) の
// 標準チェック値 "123456789" -> 0x0376E6E7 を確認する
func TestCRC32KnownVector(t *testing.T) {
	if got := CRC32([]byte("123456789")); got != 0x0376E6E7 {
		t.Fatalf("CRC32(123456789) = %08X, want 0376E6E7", got)
	}
	if got := CRC32(nil); got != 0xFFFFFFFF {
		t.Fatalf("CRC32(nil) = %08X, want FFFFFFFF", got)
	}
}

// TestCRC32SectionWithCRCIsZero は CRC を末尾に付加したセクション全体の CRC が 0 になることを確認する
func TestCRC32SectionWithCRCIsZero(t *testing.T) {
	body := []byte{0x00, 0xB0, 0x0D, 0x7F, 0xE1, 0xC1, 0x00, 0x00, 0x00, 0x00, 0xE0, 0x10}
	c := CRC32(body)
	sec := append(append([]byte{}, body...), byte(c>>24), byte(c>>16), byte(c>>8), byte(c))
	if got := CRC32(sec); got != 0 {
		t.Fatalf("CRC32(section+crc) = %08X, want 0", got)
	}
	sec[5] ^= 1
	if CRC32(sec) == 0 {
		t.Fatalf("corrupted section must not have zero CRC")
	}
}
