package aribts

import "testing"

// TestDecodeStringNodeParity は Node 参照実装で生成した期待値と DecodeString の結果を突き合わせる。
// 期待値は .hermes-development/scratch/aribts/gen_charcases.js が Node 版 TsChar から生成した
// char_gen_test.go に格納されている。
func TestDecodeStringNodeParity(t *testing.T) {
	for _, tc := range charExpectedCases {
		got := DecodeString(tc.in)
		if got != tc.want {
			t.Errorf("DecodeString(%s) = %q, want %q (input % x)", tc.name, got, tc.want, tc.in)
		}
	}
}

// TestSjisDecode は shift-jis デコードの基本挙動 (未定義バイトの U+FFFD 化) を確認する
func TestSjisDecode(t *testing.T) {
	// 0x82 0x9F は「ぁ」 (JIS X 0208 ひらがな)
	if got := sjisDecode([]byte{0x82, 0x9F}); got != "ぁ" {
		t.Errorf("sjisDecode(829F) = %q, want ぁ", got)
	}
	// 未定義バイト 0xA0 は U+FFFD
	if got := sjisDecode([]byte{0xA0}); got != "\uFFFD" {
		t.Errorf("sjisDecode(A0) = %q, want U+FFFD", got)
	}
	// lead バイトの後続が不正なら lead を U+FFFD にして trail を再解釈する
	if got := sjisDecode([]byte{0x81, 0x20}); got != "\uFFFD " {
		t.Errorf("sjisDecode(8120) = %q, want %q", got, "\uFFFD ")
	}
	// 末尾の不完全な lead バイトは U+FFFD
	if got := sjisDecode([]byte{0x41, 0x81}); got != "A\uFFFD" {
		t.Errorf("sjisDecode(4181) = %q, want %q", got, "A\uFFFD")
	}
}
