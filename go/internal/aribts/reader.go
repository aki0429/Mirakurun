package aribts

// reader は Node 版 TsReader のビットリーダーを癖ごと再現する。
//   - 位置 pos はビット単位で、範囲外の読み取りは 0 を返しつつ位置だけ進める
//   - readBytes は「ビット位置 + バイト長 > バイト長 << 3」で範囲判定する (単位が混在している) ため、
//     バッファ末尾付近では要求より短いスライスが返る
//   - 負の長さでは Node の Buffer#slice と同様に負のインデックスが末尾からの位置として扱われる
//   - 32bit 以上の読み取りは Node 版 readBits の 31bit 分割ロジック (乗算が先に行われる) をそのまま再現する
type reader struct {
	// buf は読み取り対象のバイト列
	buf []byte
	// pos は現在のビット位置 (負になり得る)
	pos int
}

// byteAtSigned は負のインデックスを含む範囲外アクセスに対して 0 を返す (JS の undefined をビット演算したときの値)
func (r *reader) byteAtSigned(i int) int {
	if i >= 0 && i < len(r.buf) {
		return int(r.buf[i])
	}
	return 0
}

// readBitsRaw は 31bit 以下の値を読み取る (Node 版 readBitsRaw)
func (r *reader) readBitsRaw(length int) int64 {
	if r.pos+length > len(r.buf)<<3 {
		r.pos += length
		return 0
	}
	var value int64
	for length > 7 {
		index := r.pos >> 3
		shift := uint(r.pos & 0x07)
		mask := (1 << (8 - shift)) - 1
		value <<= 8
		value |= int64((r.byteAtSigned(index) & mask) << shift)
		value |= int64(r.byteAtSigned(index+1) >> (8 - shift))
		r.pos += 8
		length -= 8
	}
	for length > 0 {
		index := r.pos >> 3
		shift := uint(r.pos&0x07) ^ 0x07
		value <<= 1
		value |= int64(r.byteAtSigned(index) >> shift & 0x01)
		r.pos++
		length--
	}
	// JS のビット演算は 32bit 符号付き整数で行われるため、それに合わせて切り詰める
	return int64(int32(value))
}

// readBits は任意長のビットを読み取る (Node 版 readBits、31bit を超える場合の分割ロジックを含む)
func (r *reader) readBits(length int) int64 {
	var value int64
	for length > 31 {
		bits := (length-1)%31 + 1
		value *= 0x80000000
		value += r.readBitsRaw(bits)
		length -= bits
	}
	value += r.readBitsRaw(length)
	return value
}

// uimsbf は符号なし整数を読み取る
func (r *reader) uimsbf(length int) int {
	return int(r.readBits(length))
}

// next は指定ビット数だけ読み飛ばす
func (r *reader) next(length int) {
	r.pos += length
}

// bytePos は現在位置をバイト単位で返す (JS の position >> 3 と同じく負方向へ切り捨て)
func (r *reader) bytePos() int {
	return r.pos >> 3
}

// readBytes は length バイトを読み取る (Node 版 readBytesRaw)。返り値は buf のスライス (コピーしない)。
func (r *reader) readBytes(length int) []byte {
	if r.pos+length > len(r.buf)<<3 {
		r.pos += length
		return r.buf[:0]
	}
	start := r.pos >> 3
	r.pos += length << 3
	return nodeSlice(r.buf, start, start+length)
}

// nodeSlice は Node の Buffer#slice(start, end) と同じ規則 (負のインデックスは末尾から、範囲は切り詰め、
// end < start は空) でスライスを返す
func nodeSlice(b []byte, start, end int) []byte {
	n := len(b)
	clamp := func(i int) int {
		if i < 0 {
			i += n
			if i < 0 {
				i = 0
			}
		}
		if i > n {
			i = n
		}
		return i
	}
	s, e := clamp(start), clamp(end)
	if e < s {
		e = s
	}
	return b[s:e]
}
