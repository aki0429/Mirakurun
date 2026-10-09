package aribts

import "strings"

// ARIB STD-B24 文字符号のデコードで用いる文字集合番号 (Node 版 char.js の charCode)
const (
	charCodeHiragana     = 0x30
	charCodeKatakana     = 0x31
	charCodePropAscii    = 0x36
	charCodePropHiragana = 0x37
	charCodePropKatakana = 0x38
	charCodeJISKanji1    = 0x39
	charCodeJISKanji2    = 0x3A
	charCodeSymbol       = 0x3B
	charCodeKanji        = 0x42
	charCodeJISX0201Kana = 0x49
	charCodeAscii        = 0x4A
)

// 文字集合の動作モード (Node 版 char.js の charMode)
const (
	charModeGraphic = 1
	charModeDRCS    = 2
	charModeOther   = 3
)

// デコード中の例外を表現するセンチネル。Node 版 decode() の try/catch を再現するために用いる。
type charDecodeError struct{}

func (charDecodeError) Error() string { return "aribts: character decode aborted" }

// tsChar は Node 版 TsChar を 1 対 1 で移植した ARIB STD-B24 デコーダ。
// graphic/graphicMode/graphicByte は G0-G3 の各文字集合の指定を保持し、
// graphicL/graphicR は現在 GL/GR に割り当てられている集合を指す。
type tsChar struct {
	// buffer はデコード対象の生バイト列
	buffer []byte
	// position は現在の読み取り位置
	position int
	// graphic は G0-G3 に指定された文字集合番号
	graphic [4]int
	// graphicMode は G0-G3 の動作モード (graphic/DRCS/other)
	graphicMode [4]int
	// graphicByte は DRCS/other のときに読み飛ばすバイト数
	graphicByte [4]int
	// graphicL は GL に使用する集合のインデックス
	graphicL int
	// graphicR は GR に使用する集合のインデックス
	graphicR int
	// graphicNormal は文字を通常の (全角) 形で出力するか
	graphicNormal bool
	// sjis は shift-jis デコードに回すまでの一時バイト列
	sjis []byte
	// result はデコード結果
	result strings.Builder
}

// DecodeString は ARIB STD-B24 の 8 単位符号列を UTF-8 文字列にデコードする。
// Node 版 new TsChar(buffer).decode() とバイト単位で同一の結果を返すことを目的としている。
func DecodeString(b []byte) string {
	c := &tsChar{
		buffer:        b,
		graphic:       [4]int{charCodeKanji, charCodeAscii, charCodeHiragana, charCodeKatakana},
		graphicMode:   [4]int{charModeGraphic, charModeGraphic, charModeGraphic, charModeGraphic},
		graphicByte:   [4]int{2, 1, 1, 1},
		graphicL:      0,
		graphicR:      2,
		graphicNormal: true,
	}
	return c.decode()
}

// getNext は次の 1 バイトを読み取る。末尾に達している場合は Node 版と同じく例外を投げる。
func (c *tsChar) getNext() int {
	if len(c.buffer) == c.position {
		panic(charDecodeError{})
	}
	b := int(c.buffer[c.position])
	c.position++
	return b
}

// pushRaw は shift-jis デコード待ちのバイト列に生バイトを追加する
func (c *tsChar) pushRaw(bs ...int) {
	for _, b := range bs {
		c.sjis = append(c.sjis, byte(b))
	}
}

// pushTable は文字テーブルの参照結果を追加する。要素が未定義 (Node では undefined) の場合は
// 例外を投げて Node 版の push(...undefined) と同じ中断挙動を再現する。
func (c *tsChar) pushTable(b []byte) {
	if b == nil {
		panic(charDecodeError{})
	}
	c.sjis = append(c.sjis, b...)
}

// flush はたまっている shift-jis バイト列をデコードして結果へ追加する
func (c *tsChar) flush() {
	if len(c.sjis) > 0 {
		c.result.WriteString(sjisDecode(c.sjis))
		c.sjis = c.sjis[:0]
	}
}

// decode は Node 版 decode() を再現する。ループ中の例外は握りつぶし、その後必ず flush する。
func (c *tsChar) decode() string {
	func() {
		defer func() { _ = recover() }()
		for c.position < len(c.buffer) {
			b := int(c.buffer[c.position])
			switch {
			case b <= 0x20:
				c.readC0()
			case b <= 0x7E:
				c.readGL()
			case b <= 0xA0:
				c.readC1()
			case b != 0xFF:
				c.readGR()
			default:
				c.position++
			}
		}
	}()
	c.flush()
	return c.result.String()
}

// readC0 は C0 制御符号を処理する
func (c *tsChar) readC0() {
	switch c.getNext() {
	case 0x20:
		// 空白: 通常時は全角空白、非通常時は半角空白
		if c.graphicNormal {
			c.pushRaw(0x81, 0x40)
		} else {
			c.pushRaw(0x20)
		}
	case 0x0D:
		c.pushRaw(0x0D, 0x0A)
	case 0x0E:
		c.graphicL = 1
	case 0x0F:
		c.graphicL = 0
	case 0x19:
		c.readSS2()
	case 0x1D:
		c.readSS3()
	case 0x1B:
		c.readESC()
	case 0x16:
		c.position++
	case 0x1C:
		c.position += 2
	}
}

// readC1 は C1 制御符号を処理する
func (c *tsChar) readC1() {
	switch c.getNext() {
	case 0x89:
		c.graphicNormal = false
	case 0x8A:
		c.graphicNormal = true
	case 0x88:
		c.graphicNormal = false
	case 0x8B:
		c.graphicNormal = c.getNext() != 0x60
	case 0x90:
		if c.getNext() == 0x20 {
			c.position++
		}
	case 0x91:
		c.position++
	case 0x93:
		c.position++
	case 0x94:
		c.position++
	case 0x95:
		for c.position < len(c.buffer) && c.buffer[c.position] != 0x4F {
			c.position++
		}
	case 0x97:
		c.position++
	case 0x98:
		c.position++
	case 0x9D:
		if c.getNext() == 0x20 {
			c.position++
		} else {
			// Node 版では 0x40 未満かつ 0x43 超という成立しない条件のため実質何もしない
			for c.position < len(c.buffer) && c.buffer[c.position] < 0x40 && c.buffer[c.position] > 0x43 {
				c.position++
			}
		}
	case 0x9B:
		c.readCSI()
	}
}

// readGL は GL に指定された文字集合で 1 文字を処理する
func (c *tsChar) readGL() {
	switch c.graphicMode[c.graphicL] {
	case charModeGraphic:
		switch c.graphic[c.graphicL] {
		case charCodePropAscii, charCodeAscii, charCodeJISX0201Kana:
			if c.graphicNormal {
				c.pushTable(charTableAscii[c.getNext()])
			} else {
				c.pushRaw(c.getNext())
			}
		case charCodeHiragana, charCodePropHiragana:
			c.pushTable(charTableHiragana[c.getNext()])
		case charCodeKatakana, charCodePropKatakana:
			c.pushTable(charTableKatakana[c.getNext()])
		case charCodeJISKanji1, charCodeJISKanji2, charCodeSymbol, charCodeKanji:
			first := c.getNext()
			second := c.getNext()
			if useUnicode(first, second) {
				c.flush()
				c.result.WriteString(getUnicode(first, second))
			} else {
				c.pushRaw(getSjis(first, second)...)
			}
		}
	default:
		// DRCS / other は読み飛ばし (Node 版と同じく出力しない)
		c.position += c.graphicByte[c.graphicL]
	}
}

// readGR は GR に指定された文字集合で 1 文字を処理する
func (c *tsChar) readGR() {
	switch c.graphicMode[c.graphicR] {
	case charModeGraphic:
		switch c.graphic[c.graphicR] {
		case charCodePropAscii, charCodeAscii:
			if c.graphicNormal {
				c.pushTable(charTableAscii[c.getNext()&0x7F])
			} else {
				c.pushRaw(c.getNext() & 0x7F)
			}
		case charCodeHiragana, charCodePropHiragana:
			c.pushTable(charTableHiragana[c.getNext()&0x7F])
		case charCodeKatakana, charCodePropKatakana, charCodeJISX0201Kana:
			c.pushTable(charTableKatakana[c.getNext()&0x7F])
		case charCodeJISKanji1, charCodeJISKanji2, charCodeSymbol, charCodeKanji:
			first := c.getNext() & 0x7F
			second := c.getNext() & 0x7F
			if useUnicode(first, second) {
				c.flush()
				c.result.WriteString(getUnicode(first, second))
			} else {
				c.pushRaw(getSjis(first, second)...)
			}
		}
	default:
		c.position += c.graphicByte[c.graphicR]
	}
}

// readESC は ESC シーケンスによる文字集合の指定を処理する
func (c *tsChar) readESC() {
	b := c.getNext()
	switch {
	case b == 0x24:
		b2 := c.getNext()
		if b2 >= 0x28 && b2 <= 0x2B {
			b3 := c.getNext()
			switch {
			case b3 == 0x20:
				b4 := c.getNext()
				c.graphic[b2-0x28] = b4
				c.graphicMode[b2-0x28] = charModeDRCS
				c.graphicByte[b2-0x28] = 2
			case b3 == 0x28:
				b4 := c.getNext()
				c.graphic[b2-0x28] = b4
				c.graphicMode[b2-0x28] = charModeOther
				c.graphicByte[b2-0x28] = 1
			default:
				c.graphic[b2-0x28] = b3
				c.graphicMode[b2-0x28] = charModeGraphic
				c.graphicByte[b2-0x28] = 2
			}
		} else {
			c.graphic[0] = b2
			c.graphicMode[0] = charModeGraphic
			c.graphicByte[0] = 2
		}
	case b >= 0x28 && b <= 0x2B:
		b2 := c.getNext()
		if b2 == 0x20 {
			b3 := c.getNext()
			c.graphic[b-0x28] = b3
			c.graphicMode[b-0x28] = charModeDRCS
			c.graphicByte[b-0x28] = 1
		} else {
			c.graphic[b-0x28] = b2
			c.graphicMode[b-0x28] = charModeGraphic
			c.graphicByte[b-0x28] = 1
		}
	case b == 0x6E:
		c.graphicL = 2
	case b == 0x6F:
		c.graphicL = 3
	case b == 0x7C:
		c.graphicR = 3
	case b == 0x7D:
		c.graphicR = 2
	case b == 0x7E:
		c.graphicR = 1
	}
}

// readSS2 は SS2 による単一シフトを処理する
func (c *tsChar) readSS2() {
	holdL := c.graphicL
	c.graphicL = 2
	c.readGL()
	c.graphicL = holdL
}

// readSS3 は SS3 による単一シフトを処理する
func (c *tsChar) readSS3() {
	holdL := c.graphicL
	c.graphicL = 3
	c.readGL()
	c.graphicL = holdL
}

// readCSI は CSI を処理する (Node 版では何もしない)
func (c *tsChar) readCSI() {}

// useUnicode は追加記号のコードが Unicode 直接マッピングの対象かを判定する
func useUnicode(first, second int) bool {
	if first >= 0x75 && second >= 0x21 {
		code := first<<8 | second
		if code >= 0x7521 && code <= 0x764B {
			return true
		}
		if code >= 0x7A4D && code <= 0x7E7D {
			return true
		}
		return false
	}
	return false
}

// getSjis は JIS コードを shift-jis バイト列へ変換する (Node 版 getSjis)。
// 追加記号の範囲 (first >= 0x75 && second >= 0x21) は useUnicode が true になるため
// ここには到達せず、範囲外として空を返す (Node 版と同じ結果)。
func getSjis(first, second int) []int {
	if first >= 0x75 && second >= 0x21 {
		return []int{}
	}
	row := 0x70
	if first >= 0x5F {
		row = 0xB0
	}
	cell := 0x7E
	if first&1 != 0 {
		if second > 0x5F {
			cell = 0x20
		} else {
			cell = 0x1F
		}
	}
	first = ((first+1)>>1 + row) & 0xFF
	second = (second + cell) & 0xFF
	return []int{first, second}
}

// getUnicode は追加記号のコードに対応する Unicode 文字列を返す (Node 版 getUnicode)。
// 範囲内だがマッピングが無い場合は Node で undefined となり、文字列連結で "undefined" になるため、
// それをそのまま再現する。範囲外は空文字列を返す。
func getUnicode(first, second int) string {
	if first >= 0x75 && second >= 0x21 {
		code := first<<8 | second
		if code >= 0x7521 && code <= 0x764B {
			if s, ok := charTableGaiji2Unicode[code]; ok {
				return s
			}
			return "undefined"
		}
		if code >= 0x7A4D && code <= 0x7E7D {
			if s, ok := charTableGaiji1Unicode[code]; ok {
				return s
			}
			return "undefined"
		}
		return ""
	}
	return ""
}

// sjisDecode は shift-jis バイト列を UTF-8 文字列へデコードする。
// Node 版は iconv-lite の shift-jis デコーダを使うため、その挙動 (未定義バイトは U+FFFD、
// lead バイトの後続が不正なら lead を U+FFFD にして trail を再解釈する) を再現する。
func sjisDecode(b []byte) string {
	var sb strings.Builder
	for i := 0; i < len(b); {
		c := int(b[i])
		if v := sjisSingle[c]; v >= 0 {
			sb.WriteRune(rune(v))
			i++
			continue
		}
		if sjisLead[c] {
			if i+1 < len(b) {
				if v, ok := sjisTwo[c<<8|int(b[i+1])]; ok {
					sb.WriteRune(rune(v))
					i += 2
					continue
				}
			}
			sb.WriteRune(0xFFFD)
			i++
			continue
		}
		sb.WriteRune(0xFFFD)
		i++
	}
	return sb.String()
}
