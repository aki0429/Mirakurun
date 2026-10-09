// Package aribts は Node.js ライブラリ @chinachu/aribts (v1.3.5-mirakurun.18) のうち、
// Mirakurun が利用する部分 (TS パケットからのセクション組み立て・PSI/SI テーブルのデコード・
// ARIB STD-B24 文字列のデコード・ロゴデータの PNG 化) をバイト互換で移植したパッケージ。
//
// Node 版の挙動 (癖を含む) と完全に一致させることを最優先としているため、
// 規格上は不自然に見える処理もあえてそのまま移植している箇所がある。
package aribts

// crc32Table は MPEG-2 CRC32 (多項式 0x04C11DB7、MSB ファースト) の 256 エントリのテーブル
var crc32Table = func() (table [256]uint32) {
	for i := range table {
		c := uint32(i) << 24
		for range 8 {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04C11DB7
			} else {
				c <<= 1
			}
		}
		table[i] = c
	}
	return table
}()

// CRC32 は MPEG-2 CRC32 を計算する (Node 版 crc32.calc と同じく初期値 0xFFFFFFFF・最終反転なし)。
// CRC_32 フィールドを含むセクション全体を渡すと、正常なセクションでは 0 を返す。
func CRC32(b []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, v := range b {
		crc = crc<<8 ^ crc32Table[byte(crc>>24)^v]
	}
	return crc
}
