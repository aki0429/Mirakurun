package aribts

// byteAt は Node の Buffer 範囲外アクセス (undefined) をビット演算した結果 (0) を再現するため、
// 範囲外のインデックスに対して 0 を返す
func byteAt(b []byte, i int) int {
	if i < len(b) {
		return int(b[i])
	}
	return 0
}

// bcd は 1 バイトの BCD を Node 版と同じ単純な算術 ((上位ニブル * 10) + 下位ニブル) で数値化する
// (0xA-0xF のニブルも検証せずにそのまま計算する)
func bcd(v int) int64 {
	return int64((v>>4)*10 + v&0x0F)
}

// MJDToUnixMs は 40bit の MJD + BCD 時分秒を UNIX ミリ秒に変換する。
// Mirakurun の common.getTimeFromMJD と同じく、入力を JST とみなして 9 時間を差し引く。
func MJDToUnixMs(b []byte) int64 {
	mjd := int64(byteAt(b, 0)<<8 | byteAt(b, 1))
	h := bcd(byteAt(b, 2))
	i := bcd(byteAt(b, 3))
	s := bcd(byteAt(b, 4))
	return ((mjd-40587)*86400 + (h-9)*3600 + i*60 + s) * 1000
}

// BCD24ToMs は 24bit BCD の時分秒 (EIT の duration など) をミリ秒に変換する
// (Mirakurun の common.getTimeFromBCD24 と同じ計算)。
func BCD24ToMs(b []byte) int64 {
	return (bcd(byteAt(b, 0))*3600 + bcd(byteAt(b, 1))*60 + bcd(byteAt(b, 2))) * 1000
}
