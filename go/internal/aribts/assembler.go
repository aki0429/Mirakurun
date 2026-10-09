package aribts

// pidState は PID ごとのセクション組み立て状態 (Node 版 TsInfo に相当) を保持する
type pidState struct {
	// counter は直前の continuity_counter (-1 は未受信またはリセット済み)
	counter int
	// duplication は同一 continuity_counter が連続した回数
	duplication int
	// kind はペイロードの種別 (0: 不明, 1: PES, 2: セクション)
	kind int
	// drops は continuity_counter の不連続によるドロップ回数
	drops int
	// buf は組み立て途中のセクションのバイト列 (Node 版 TsBuffer に相当)
	buf []byte
	// entireLength は組み立て途中のセクションの全長 (ヘッダー 3 バイトを含む)
	entireLength int
}

// SectionAssembler は TS パケット列から PSI/SI セクションを組み立てる (Node 版 TsStreamLite#write に相当)。
// PID ごとに状態を持ち、continuity_counter のドロップ・重複・discontinuity_indicator・
// スクランブル・PES 判定・pointer_field・複数セクション・0xFF スタッフィングを Node 版と同じ規則で扱う。
// ゴルーチンセーフではない。
type SectionAssembler struct {
	// states は PID (13bit) ごとの状態。初めてそのPIDのパケットを受け取ったときに確保する
	states [8192]*pidState
}

// NewSectionAssembler は空の SectionAssembler を作成する
func NewSectionAssembler() *SectionAssembler {
	return &SectionAssembler{}
}

// Drops は指定 PID で continuity_counter の不連続によりバッファを破棄した回数を返す
func (a *SectionAssembler) Drops(pid uint16) int {
	if st := a.states[pid&0x1FFF]; st != nil {
		return st.drops
	}
	return 0
}

// resetBuf は組み立て途中のセクションを破棄する (Node 版 TsBuffer#reset に相当、entireLength は変更しない)
func (st *pidState) resetBuf() {
	st.buf = st.buf[:0]
}

// cloneBytes は保持しても安全なようにバイト列をコピーする
func cloneBytes(b []byte) []byte {
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

// packetData は TS パケットのペイロード部分を返す (Node 版 TsPacket.getData に相当)。
// アダプテーションフィールド長が異常でペイロード開始位置が 188 を超える場合は空スライスを返す。
func packetData(p []byte) []byte {
	start := 4
	if p[3]&0x20 != 0 {
		start = 5 + int(p[4])
	}
	if start > len(p) {
		return p[len(p):]
	}
	return p[start:]
}

// isPES は PUSI パケットのペイロードが PES (packet_start_code_prefix 0x000001) で始まるかを判定する
// (Node 版 TsPacket.isPes に相当、範囲外アクセスは undefined 扱いで false)
func isPES(p []byte) bool {
	off := 4
	if p[3]&0x20 != 0 {
		off = 5 + int(p[4])
	}
	return off+2 < len(p) && p[off] == 0x00 && p[off+1] == 0x00 && p[off+2] == 0x01
}

// Push は 188 バイトの TS パケットを 1 つ投入し、そのパケットで完成したセクションを返す。
// 返すセクションはすべてコピーなので呼び出し側で保持してよい。セクションを含まないパケットではアロケーションしない。
func (a *SectionAssembler) Push(packet []byte) (pid uint16, sections [][]byte) {
	if len(packet) < 188 {
		return 0, nil
	}
	pid = uint16(packet[1]&0x1F)<<8 | uint16(packet[2])

	// transport_error_indicator が立っているパケットは状態も更新せずに捨てる
	if packet[1]&0x80 != 0 {
		return pid, nil
	}
	st := a.states[pid]
	if st == nil {
		st = &pidState{counter: -1}
		a.states[pid] = st
	}

	// ペイロードがないパケット (adaptation_field_control = 0b10/0b00) は何もしない
	afc := packet[3] >> 4 & 0x03
	if afc&0x01 == 0 {
		return pid, nil
	}

	// discontinuity_indicator が立っていれば continuity_counter の検査をリセットする
	if afc&0x02 != 0 && packet[4] != 0 && packet[5]&0x80 != 0 {
		st.counter = -1
	}

	// continuity_counter を検査し、ドロップ (または 2 回以上の重複) なら組み立て中のセクションを破棄する
	cc := int(packet[3] & 0x0F)
	if st.counter != -1 && pid != 0x1FFF {
		previous := st.counter
		expected := (previous + 1) & 0x0F
		check := true
		if cc == previous {
			st.duplication++
			if st.duplication > 1 {
				check = false
			}
		} else {
			st.duplication = 0
			if cc != expected {
				check = false
			}
		}
		if !check {
			st.drops++
			st.kind = 0
			st.resetBuf()
		}
	}
	st.counter = cc

	// スクランブルされたパケット (transport_scrambling_control の上位ビットが 1) は扱わない
	if packet[3]>>7 == 1 {
		return pid, nil
	}

	if packet[1]&0x40 != 0 {
		// payload_unit_start_indicator が立っている: PES かセクションかを判定する
		if isPES(packet) {
			st.kind = 1
			return pid, nil
		}
		st.kind = 2
		data := packetData(packet)

		// ペイロードが空の場合、Node 版では pointer_field が undefined になり以降の処理が実質スキップされる
		if len(data) == 0 {
			if len(st.buf) != 0 {
				st.kind = 0
				st.resetBuf()
				st.entireLength = 0
			}
			return pid, nil
		}
		pointerField := int(data[0])
		bytesRead := 1

		// pointer_field が前のセクションの残りバイト数と一致すれば、前のセクションを完成させる
		if pointerField != 0 && len(st.buf) != 0 {
			if st.entireLength-len(st.buf) == pointerField {
				end := min(bytesRead+pointerField, len(data))
				st.buf = append(st.buf, data[bytesRead:end]...)
				sections = append(sections, cloneBytes(st.buf))
			} else {
				st.kind = 0
			}
		}
		if len(st.buf) != 0 {
			st.resetBuf()
			st.entireLength = 0
		}
		bytesRead += pointerField

		// 0xFF (スタッフィング) が現れるまで、パケット内のセクションを順に切り出す
		for len(data) >= bytesRead+3 && data[bytesRead] != 0xFF {
			sectionLength := 3 + (int(data[bytesRead+1]&0x0F)<<8 | int(data[bytesRead+2]))
			if len(data) < bytesRead+sectionLength {
				st.buf = append(st.buf, data[bytesRead:]...)
				st.entireLength = sectionLength
				break
			}
			sections = append(sections, cloneBytes(data[bytesRead:bytesRead+sectionLength]))
			bytesRead += sectionLength
		}
		return pid, sections
	}

	// 後続パケット: セクションの組み立て中であれば続きを追加する
	if st.kind == 2 && len(st.buf) != 0 {
		data := packetData(packet)
		restLength := st.entireLength - len(st.buf)
		if len(data) < restLength {
			st.buf = append(st.buf, data...)
		} else {
			st.buf = append(st.buf, data[:restLength]...)
			sections = append(sections, cloneBytes(st.buf))
			st.resetBuf()
			st.entireLength = 0
		}
	}
	return pid, sections
}
