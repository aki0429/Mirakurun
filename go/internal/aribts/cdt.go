package aribts

// CDT は Common Data Table (table_id 0xC8) のデコード結果
type CDT struct {
	TableID                int
	SectionSyntaxIndicator int
	SectionLength          int
	DownloadDataID         int
	VersionNumber          int
	CurrentNextIndicator   int
	SectionNumber          int
	LastSectionNumber      int
	OriginalNetworkID      int
	DataType               int
	DescriptorsLoopLength  int
	Descriptors            []Descriptor
	// DataModuleByte は data_module_byte (セクション末尾のモジュールデータ)
	DataModuleByte []byte
	// CRC32 は CRC_32 フィールド (4 バイト)
	CRC32 []byte
	// Raw はセクション全体 (入力スライスを参照)
	Raw []byte
}

// DecodeCDT は CDT セクションをデコードする (Node 版 table/cdt.decode)。CRC 不一致なら ErrCRC を返す。
func DecodeCDT(sec []byte) (*CDT, error) {
	if CRC32(sec) != 0 {
		return nil, ErrCRC
	}
	r := &reader{buf: sec}
	cdt := &CDT{Raw: sec}
	cdt.TableID = r.uimsbf(8)
	cdt.SectionSyntaxIndicator = r.uimsbf(1)
	r.next(1)
	r.next(2)
	cdt.SectionLength = r.uimsbf(12)
	cdt.DownloadDataID = r.uimsbf(16)
	r.next(2)
	cdt.VersionNumber = r.uimsbf(5)
	cdt.CurrentNextIndicator = r.uimsbf(1)
	cdt.SectionNumber = r.uimsbf(8)
	cdt.LastSectionNumber = r.uimsbf(8)
	cdt.OriginalNetworkID = r.uimsbf(16)
	cdt.DataType = r.uimsbf(8)
	r.next(4)
	cdt.DescriptorsLoopLength = r.uimsbf(12)
	cdt.Descriptors = DecodeDescriptors(r.readBytes(cdt.DescriptorsLoopLength))
	cdt.DataModuleByte = r.readBytes(3 + cdt.SectionLength - r.bytePos() - 4)
	cdt.CRC32 = r.readBytes(4)
	return cdt, nil
}
