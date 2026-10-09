package aribts

// EITEvent は EIT の 1 イベント分のエントリ (Node 版 eit.js の events[] 要素)
type EITEvent struct {
	// EventID は event_id
	EventID int
	// StartTime は start_time (MJD + BCD 時分秒の 5 バイト)
	StartTime [5]byte
	// Duration は duration (BCD 時分秒の 3 バイト)
	Duration [3]byte
	// RunningStatus は running_status
	RunningStatus int
	// FreeCAMode は free_CA_mode
	FreeCAMode int
	// DescriptorsLoopLength は descriptors_loop_length
	DescriptorsLoopLength int
	// Descriptors は記述子ループのデコード結果
	Descriptors []Descriptor
}

// EIT は Event Information Table (table_id 0x4E-0x6F) のデコード結果
type EIT struct {
	TableID                  int
	SectionSyntaxIndicator   int
	SectionLength            int
	ServiceID                int
	VersionNumber            int
	CurrentNextIndicator     int
	SectionNumber            int
	LastSectionNumber        int
	TransportStreamID        int
	OriginalNetworkID        int
	SegmentLastSectionNumber int
	LastTableID              int
	Events                   []EITEvent
	// CRC32 は CRC_32 フィールド (4 バイト)
	CRC32 []byte
	// Raw はセクション全体 (入力スライスを参照)
	Raw []byte
}

// copyFixed は src の先頭を dst へコピーする (src が短い場合は残りを 0 のままにする)
func copyFixed(dst []byte, src []byte) {
	copy(dst, src)
}

// DecodeEIT は EIT セクションをデコードする (Node 版 table/eit.decode)。CRC 不一致なら ErrCRC を返す。
func DecodeEIT(sec []byte) (*EIT, error) {
	if CRC32(sec) != 0 {
		return nil, ErrCRC
	}
	r := &reader{buf: sec}
	eit := &EIT{Raw: sec, Events: []EITEvent{}}
	eit.TableID = r.uimsbf(8)
	eit.SectionSyntaxIndicator = r.uimsbf(1)
	r.next(1)
	r.next(2)
	eit.SectionLength = r.uimsbf(12)
	eit.ServiceID = r.uimsbf(16)
	r.next(2)
	eit.VersionNumber = r.uimsbf(5)
	eit.CurrentNextIndicator = r.uimsbf(1)
	eit.SectionNumber = r.uimsbf(8)
	eit.LastSectionNumber = r.uimsbf(8)
	eit.TransportStreamID = r.uimsbf(16)
	eit.OriginalNetworkID = r.uimsbf(16)
	eit.SegmentLastSectionNumber = r.uimsbf(8)
	eit.LastTableID = r.uimsbf(8)
	for r.bytePos() < 3+eit.SectionLength-4 {
		var ev EITEvent
		ev.EventID = r.uimsbf(16)
		copyFixed(ev.StartTime[:], r.readBytes(5))
		copyFixed(ev.Duration[:], r.readBytes(3))
		ev.RunningStatus = r.uimsbf(3)
		ev.FreeCAMode = r.uimsbf(1)
		ev.DescriptorsLoopLength = r.uimsbf(12)
		ev.Descriptors = DecodeDescriptors(r.readBytes(ev.DescriptorsLoopLength))
		eit.Events = append(eit.Events, ev)
	}
	eit.CRC32 = r.readBytes(4)
	return eit, nil
}
