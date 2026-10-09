package aribts

// TOT は Time Offset Table (table_id 0x73) のデコード結果
type TOT struct {
	TableID                int
	SectionSyntaxIndicator int
	SectionLength          int
	// JSTTime は JST_time (MJD + BCD 時分秒の 5 バイト)
	JSTTime               [5]byte
	DescriptorsLoopLength int
	Descriptors           []Descriptor
	// CRC32 は CRC_32 フィールド (4 バイト)
	CRC32 []byte
	// Raw はセクション全体 (入力スライスを参照)
	Raw []byte
}

// DecodeTOT は TOT セクションをデコードする (Node 版 table/tot.decode)。CRC 不一致なら ErrCRC を返す。
func DecodeTOT(sec []byte) (*TOT, error) {
	if CRC32(sec) != 0 {
		return nil, ErrCRC
	}
	r := &reader{buf: sec}
	tot := &TOT{Raw: sec}
	tot.TableID = r.uimsbf(8)
	tot.SectionSyntaxIndicator = r.uimsbf(1)
	r.next(1)
	r.next(2)
	tot.SectionLength = r.uimsbf(12)
	copyFixed(tot.JSTTime[:], r.readBytes(5))
	r.next(4)
	tot.DescriptorsLoopLength = r.uimsbf(12)
	tot.Descriptors = DecodeDescriptors(r.readBytes(tot.DescriptorsLoopLength))
	tot.CRC32 = r.readBytes(4)
	return tot, nil
}
