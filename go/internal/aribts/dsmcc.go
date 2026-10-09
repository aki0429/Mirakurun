package aribts

// DSMCCModuleDescriptor は DSM-CC のモジュール記述子 (Node 版 module_descriptors + module_descriptor/*)
type DSMCCModuleDescriptor struct {
	// Raw は記述子全体 (タグ・長さを含む、Node 版 _raw)
	Raw []byte
	// Tag は descriptor_tag
	Tag int
	// Length は descriptor_length
	Length int
	// TextChar はタグ 0x01 (type) / 0x02 (name) の text_char
	TextChar []byte
	// ISO639LanguageCode はタグ 0x03 (info) の ISO_639_language_code
	ISO639LanguageCode []byte
	// Position / ModuleID はタグ 0x04 (module_link)
	Position int
	ModuleID int
	// CRC32 はタグ 0x05 (crc32) の CRC_32
	CRC32 []byte
	// Descriptor は未知タグの descriptor バイト列
	Descriptor []byte
}

// decodeModuleDescriptors はモジュール記述子ループをデコードする (Node 版 TsModuleDescriptors#decode)
func decodeModuleDescriptors(b []byte) []DSMCCModuleDescriptor {
	ds := []DSMCCModuleDescriptor{}
	for bytesRead := 0; bytesRead < len(b); {
		tag := int(b[bytesRead])
		bytesRead++
		if bytesRead >= len(b) {
			// descriptor_length が欠けている: Node 版では slice(x, NaN) で空になりループ終了
			ds = append(ds, decodeModuleDescriptor(tag, b[:0]))
			break
		}
		length := int(b[bytesRead])
		bytesRead++
		raw := nodeSlice(b, bytesRead-2, bytesRead+length)
		bytesRead += length
		ds = append(ds, decodeModuleDescriptor(tag, raw))
	}
	return ds
}

// decodeModuleDescriptor はタグに応じて 1 モジュール記述子をデコードする
func decodeModuleDescriptor(tag int, raw []byte) DSMCCModuleDescriptor {
	r := &reader{buf: raw}
	d := DSMCCModuleDescriptor{Raw: raw}
	d.Tag = r.uimsbf(8)
	d.Length = r.uimsbf(8)
	length := d.Length
	switch tag {
	case 0x01, 0x02:
		d.TextChar = r.readBytes(length)
	case 0x03:
		d.ISO639LanguageCode = r.readBytes(3)
		d.TextChar = restBytes(r, length)
	case 0x04:
		d.Position = r.uimsbf(8)
		d.ModuleID = r.uimsbf(16)
	case 0x05:
		d.CRC32 = r.readBytes(4)
	default:
		d.Descriptor = r.readBytes(length)
	}
	return d
}

// DSMCCCompatibilitySubDescriptor は互換性記述子のサブ記述子
type DSMCCCompatibilitySubDescriptor struct {
	SubDescriptorType     int
	SubDescriptorLength   int
	AdditionalInformation []byte
}

// DSMCCCompatibilityItem は互換性記述子の 1 記述子
type DSMCCCompatibilityItem struct {
	DescriptorType     int
	DescriptorLength   int
	SpecifierType      int
	SpecifierData      []byte
	Model              int
	Version            int
	SubDescriptorCount int
	SubDescriptors     []DSMCCCompatibilitySubDescriptor
}

// DSMCCCompatibility は互換性記述子 (Node 版 TsDescriptorCompatibility)
type DSMCCCompatibility struct {
	Raw                           []byte
	CompatibilityDescriptorLength int
	DescriptorCount               int
	Descriptors                   []DSMCCCompatibilityItem
}

// decodeCompatibility は互換性記述子をデコードする
func decodeCompatibility(b []byte) *DSMCCCompatibility {
	r := &reader{buf: b}
	cd := &DSMCCCompatibility{Raw: b, Descriptors: []DSMCCCompatibilityItem{}}
	cd.CompatibilityDescriptorLength = r.uimsbf(16)
	cd.DescriptorCount = r.uimsbf(16)
	for range cd.DescriptorCount {
		item := DSMCCCompatibilityItem{SubDescriptors: []DSMCCCompatibilitySubDescriptor{}}
		item.DescriptorType = r.uimsbf(8)
		item.DescriptorLength = r.uimsbf(8)
		item.SpecifierType = r.uimsbf(8)
		item.SpecifierData = r.readBytes(3)
		item.Model = r.uimsbf(16)
		item.Version = r.uimsbf(16)
		item.SubDescriptorCount = r.uimsbf(8)
		for range item.SubDescriptorCount {
			var sub DSMCCCompatibilitySubDescriptor
			sub.SubDescriptorType = r.uimsbf(8)
			sub.SubDescriptorLength = r.uimsbf(8)
			sub.AdditionalInformation = r.readBytes(sub.SubDescriptorLength)
			item.SubDescriptors = append(item.SubDescriptors, sub)
		}
		cd.Descriptors = append(cd.Descriptors, item)
	}
	return cd
}

// DSMCCModule は DII の 1 モジュール
type DSMCCModule struct {
	ModuleID         int
	ModuleSize       int
	ModuleVersion    int
	ModuleInfoLength int
	ModuleInfo       []DSMCCModuleDescriptor
}

// DSMCCDownloadInfoIndication は DSM-CC DII メッセージ (table_id 0x3B)
type DSMCCDownloadInfoIndication struct {
	ProtocolDiscriminator   int
	DSMCCType               int
	MessageID               int
	TransactionID           int
	AdaptationLength        int
	MessageLength           int
	AdaptationType          int
	AdaptationDataByte      []byte
	DownloadID              int
	BlockSize               int
	WindowSize              int
	AckPeriod               int
	TCDownloadWindow        int
	TCDownloadScenario      int
	CompatibilityDescriptor *DSMCCCompatibility
	NumberOfModules         int
	Modules                 []DSMCCModule
	PrivateDataLength       int
	PrivateData             []DSMCCModuleDescriptor
}

// decodeDII は DII メッセージをデコードする (Node 版 TsDsmccMessageDownloadInfoIndication#decode)
func decodeDII(b []byte) *DSMCCDownloadInfoIndication {
	r := &reader{buf: b}
	dii := &DSMCCDownloadInfoIndication{Modules: []DSMCCModule{}, PrivateData: []DSMCCModuleDescriptor{}}
	dii.ProtocolDiscriminator = r.uimsbf(8)
	dii.DSMCCType = r.uimsbf(8)
	dii.MessageID = r.uimsbf(16)
	dii.TransactionID = r.uimsbf(32)
	r.next(8)
	dii.AdaptationLength = r.uimsbf(8)
	dii.MessageLength = r.uimsbf(16)
	if dii.AdaptationLength > 0 {
		dii.AdaptationType = r.uimsbf(8)
		dii.AdaptationDataByte = r.readBytes(dii.AdaptationLength - 1)
	}
	dii.DownloadID = r.uimsbf(32)
	dii.BlockSize = r.uimsbf(16)
	dii.WindowSize = r.uimsbf(8)
	dii.AckPeriod = r.uimsbf(8)
	dii.TCDownloadWindow = r.uimsbf(32)
	dii.TCDownloadScenario = r.uimsbf(32)
	// 互換性記述子は長さを直接バッファから読む (範囲外は Node の undefined と同様に 0 扱い)
	descriptorLength := byteAt(r.buf, r.bytePos())<<8 | byteAt(r.buf, r.bytePos()+1)
	dii.CompatibilityDescriptor = decodeCompatibility(r.readBytes(2 + descriptorLength))
	dii.NumberOfModules = r.uimsbf(16)
	for range dii.NumberOfModules {
		var m DSMCCModule
		m.ModuleID = r.uimsbf(16)
		m.ModuleSize = r.uimsbf(32)
		m.ModuleVersion = r.uimsbf(8)
		m.ModuleInfoLength = r.uimsbf(8)
		m.ModuleInfo = decodeModuleDescriptors(r.readBytes(m.ModuleInfoLength))
		dii.Modules = append(dii.Modules, m)
	}
	dii.PrivateDataLength = r.uimsbf(16)
	dii.PrivateData = decodeModuleDescriptors(r.readBytes(dii.PrivateDataLength))
	return dii
}

// DSMCCDownloadDataBlock は DSM-CC DDB メッセージ (table_id 0x3C)
type DSMCCDownloadDataBlock struct {
	ProtocolDiscriminator int
	DSMCCType             int
	MessageID             int
	DownloadID            int
	AdaptationLength      int
	MessageLength         int
	AdaptationType        int
	AdaptationDataByte    []byte
	ModuleID              int
	ModuleVersion         int
	BlockNumber           int
	BlockDataByte         []byte
}

// decodeDDB は DDB メッセージをデコードする (Node 版 TsDsmccMessageDownloadDataBlock#decode)
func decodeDDB(b []byte) *DSMCCDownloadDataBlock {
	r := &reader{buf: b}
	ddb := &DSMCCDownloadDataBlock{}
	ddb.ProtocolDiscriminator = r.uimsbf(8)
	ddb.DSMCCType = r.uimsbf(8)
	ddb.MessageID = r.uimsbf(16)
	ddb.DownloadID = r.uimsbf(32)
	r.next(8)
	ddb.AdaptationLength = r.uimsbf(8)
	ddb.MessageLength = r.uimsbf(16)
	if ddb.AdaptationLength > 0 {
		ddb.AdaptationType = r.uimsbf(8)
		ddb.AdaptationDataByte = r.readBytes(ddb.AdaptationLength - 1)
	}
	ddb.ModuleID = r.uimsbf(16)
	ddb.ModuleVersion = r.uimsbf(8)
	r.next(8)
	ddb.BlockNumber = r.uimsbf(16)
	ddb.BlockDataByte = r.readBytes(ddb.MessageLength - ddb.AdaptationLength - 6)
	return ddb
}

// DSMCC は DSM-CC セクション (table_id 0x3A-0x3F) のデコード結果
type DSMCC struct {
	TableID                int
	SectionSyntaxIndicator int
	PrivateIndicator       int
	DSMCCSectionLength     int
	TableIDExtension       int
	VersionNumber          int
	CurrentNextIndicator   int
	SectionNumber          int
	LastSectionNumber      int
	// Message は table_id が 0x3B のとき *DSMCCDownloadInfoIndication、0x3C のとき *DSMCCDownloadDataBlock になる
	Message any
	// StreamDescriptor は table_id 0x3D のときの本文
	StreamDescriptor []byte
	// PrivateDataByte は table_id 0x3E のときの本文
	PrivateDataByte []byte
	// Checksum は section_syntax_indicator が 0 のときの Checksum
	Checksum []byte
	// CRC32 は section_syntax_indicator が 1 のときの CRC_32
	CRC32 []byte
	// Raw はセクション全体 (入力スライスを参照)
	Raw []byte
}

// DecodeDSMCC は DSM-CC セクションをデコードする (Node 版 table/dsmcc.decode)。
// section_syntax_indicator が 1 のときに CRC 不一致なら ErrCRC を返す。
func DecodeDSMCC(sec []byte) (*DSMCC, error) {
	if len(sec) > 1 && sec[1]>>7 == 1 && CRC32(sec) != 0 {
		return nil, ErrCRC
	}
	r := &reader{buf: sec}
	d := &DSMCC{Raw: sec}
	d.TableID = r.uimsbf(8)
	d.SectionSyntaxIndicator = r.uimsbf(1)
	d.PrivateIndicator = r.uimsbf(1)
	r.next(2)
	d.DSMCCSectionLength = r.uimsbf(12)
	d.TableIDExtension = r.uimsbf(16)
	r.next(2)
	d.VersionNumber = r.uimsbf(5)
	d.CurrentNextIndicator = r.uimsbf(1)
	d.SectionNumber = r.uimsbf(8)
	d.LastSectionNumber = r.uimsbf(8)
	body := r.readBytes(3 + d.DSMCCSectionLength - r.bytePos() - 4)
	switch d.TableID {
	case 0x3B:
		d.Message = decodeDII(body)
	case 0x3C:
		d.Message = decodeDDB(body)
	case 0x3D:
		d.StreamDescriptor = body
	case 0x3E:
		d.PrivateDataByte = body
	}
	if d.SectionSyntaxIndicator == 0 {
		d.Checksum = r.readBytes(4)
	} else {
		d.CRC32 = r.readBytes(4)
	}
	return d, nil
}
