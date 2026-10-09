package aribts

// PMTStream は PMT の 1 エレメンタリーストリーム分のエントリ
type PMTStream struct {
	StreamType    int
	ElementaryPID int
	ESInfoLength  int
	ESInfo        []Descriptor
}

// PMT は Program Map Table (table_id 0x02) のデコード結果
type PMT struct {
	TableID              int
	SectionLength        int
	ProgramNumber        int
	VersionNumber        int
	CurrentNextIndicator int
	SectionNumber        int
	LastSectionNumber    int
	PCRPID               int
	ProgramInfoLength    int
	ProgramInfo          []Descriptor
	Streams              []PMTStream
	// Raw はセクション全体 (入力スライスを参照)
	Raw []byte
}

// DecodePMT は PMT セクションをデコードする (Node 版 TsTablePmt#decode)。CRC 不一致なら ErrCRC を返す。
func DecodePMT(sec []byte) (*PMT, error) {
	if CRC32(sec) != 0 {
		return nil, ErrCRC
	}
	r := &reader{buf: sec}
	pmt := &PMT{Raw: sec, Streams: []PMTStream{}}
	pmt.TableID = r.uimsbf(8)
	r.next(4)
	pmt.SectionLength = r.uimsbf(12)
	pmt.ProgramNumber = r.uimsbf(16)
	r.next(2)
	pmt.VersionNumber = r.uimsbf(5)
	pmt.CurrentNextIndicator = r.uimsbf(1)
	pmt.SectionNumber = r.uimsbf(8)
	pmt.LastSectionNumber = r.uimsbf(8)
	r.next(3)
	pmt.PCRPID = r.uimsbf(13)
	r.next(4)
	pmt.ProgramInfoLength = r.uimsbf(12)
	pmt.ProgramInfo = DecodeDescriptors(r.readBytes(pmt.ProgramInfoLength))
	for r.bytePos() < 3+pmt.SectionLength-4 {
		var s PMTStream
		s.StreamType = r.uimsbf(8)
		r.next(3)
		s.ElementaryPID = r.uimsbf(13)
		r.next(4)
		s.ESInfoLength = r.uimsbf(12)
		s.ESInfo = DecodeDescriptors(r.readBytes(s.ESInfoLength))
		pmt.Streams = append(pmt.Streams, s)
	}
	return pmt, nil
}

// NITTransportStream は NIT のトランスポートストリームループの 1 エントリ
type NITTransportStream struct {
	TransportStreamID int
	OriginalNetworkID int
	Descriptors       []Descriptor
}

// NIT は Network Information Table (table_id 0x40/0x41) のデコード結果
type NIT struct {
	TableID              int
	SectionLength        int
	NetworkID            int
	VersionNumber        int
	CurrentNextIndicator int
	SectionNumber        int
	LastSectionNumber    int
	NetworkDescriptors   []Descriptor
	TransportStreams     []NITTransportStream
	Raw                  []byte
}

// DecodeNIT は NIT セクションをデコードする (Node 版 TsTableNit#decode)。CRC 不一致なら ErrCRC を返す。
func DecodeNIT(sec []byte) (*NIT, error) {
	if CRC32(sec) != 0 {
		return nil, ErrCRC
	}
	r := &reader{buf: sec}
	nit := &NIT{Raw: sec, TransportStreams: []NITTransportStream{}}
	nit.TableID = r.uimsbf(8)
	r.next(4)
	nit.SectionLength = r.uimsbf(12)
	nit.NetworkID = r.uimsbf(16)
	r.next(2)
	nit.VersionNumber = r.uimsbf(5)
	nit.CurrentNextIndicator = r.uimsbf(1)
	nit.SectionNumber = r.uimsbf(8)
	nit.LastSectionNumber = r.uimsbf(8)
	r.next(4)
	nit.NetworkDescriptors = DecodeDescriptors(r.readBytes(r.uimsbf(12)))
	r.next(4)
	loopLength := r.uimsbf(12)
	for end := r.bytePos() + loopLength; r.bytePos() < end; {
		var ts NITTransportStream
		ts.TransportStreamID = r.uimsbf(16)
		ts.OriginalNetworkID = r.uimsbf(16)
		r.next(4)
		ts.Descriptors = DecodeDescriptors(r.readBytes(r.uimsbf(12)))
		nit.TransportStreams = append(nit.TransportStreams, ts)
	}
	return nit, nil
}

// SDTService は SDT のサービスループの 1 エントリ
type SDTService struct {
	ServiceID               int
	EITUserDefinedFlags     int
	EITScheduleFlag         int
	EITPresentFollowingFlag int
	RunningStatus           int
	FreeCAMode              int
	Descriptors             []Descriptor
}

// SDT は Service Description Table (table_id 0x42/0x46) のデコード結果
type SDT struct {
	TableID              int
	SectionLength        int
	TransportStreamID    int
	VersionNumber        int
	CurrentNextIndicator int
	SectionNumber        int
	LastSectionNumber    int
	OriginalNetworkID    int
	Services             []SDTService
	Raw                  []byte
}

// DecodeSDT は SDT セクションをデコードする (Node 版 TsTableSdt#decode)。CRC 不一致なら ErrCRC を返す。
func DecodeSDT(sec []byte) (*SDT, error) {
	if CRC32(sec) != 0 {
		return nil, ErrCRC
	}
	r := &reader{buf: sec}
	sdt := &SDT{Raw: sec, Services: []SDTService{}}
	sdt.TableID = r.uimsbf(8)
	r.next(4)
	sdt.SectionLength = r.uimsbf(12)
	sdt.TransportStreamID = r.uimsbf(16)
	r.next(2)
	sdt.VersionNumber = r.uimsbf(5)
	sdt.CurrentNextIndicator = r.uimsbf(1)
	sdt.SectionNumber = r.uimsbf(8)
	sdt.LastSectionNumber = r.uimsbf(8)
	sdt.OriginalNetworkID = r.uimsbf(16)
	r.next(8)
	for r.bytePos() < 3+sdt.SectionLength-4 {
		var s SDTService
		s.ServiceID = r.uimsbf(16)
		r.next(3)
		s.EITUserDefinedFlags = r.uimsbf(3)
		s.EITScheduleFlag = r.uimsbf(1)
		s.EITPresentFollowingFlag = r.uimsbf(1)
		s.RunningStatus = r.uimsbf(3)
		s.FreeCAMode = r.uimsbf(1)
		s.Descriptors = DecodeDescriptors(r.readBytes(r.uimsbf(12)))
		sdt.Services = append(sdt.Services, s)
	}
	return sdt, nil
}
