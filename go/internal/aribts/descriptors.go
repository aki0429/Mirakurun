package aribts

// Descriptor は記述子ループ内の 1 記述子のデコード結果 (Node 版 TsDescriptors#decode の配列要素に相当)。
// Value には Mirakurun が利用する記述子のみ型付きの値 (*ServiceDescriptor など) が入り、
// それ以外のタグでは nil になる (Raw から必要に応じて再解析できる)。
// バイト列のフィールドはすべて入力セクションのスライスを参照する (コピーしない)。
type Descriptor struct {
	// Tag は記述子バッファから読み取った descriptor_tag (末尾で長さが欠けている場合 Node 版と同じく 0 になる)
	Tag int
	// Length は descriptor_length
	Length int
	// Raw は記述子全体 (タグ・長さを含む、Node 版 _raw)
	Raw []byte
	// Null はタグ 0x00/0x01 で Node 版が undefined を配列に積むケースを表す
	Null bool
	// Value は型付きのデコード結果
	Value any
}

// ServiceDescriptor はサービス記述子 (0x48)
type ServiceDescriptor struct {
	ServiceType             int
	ServiceProviderNameChar []byte
	ServiceNameChar         []byte
}

// ShortEvent は短形式イベント記述子 (0x4D)
type ShortEvent struct {
	ISO639LanguageCode []byte
	EventNameChar      []byte
	TextChar           []byte
}

// ExtendedEventItem は拡張形式イベント記述子の 1 項目
type ExtendedEventItem struct {
	// DescriptionLength は item_description_length (0 の場合は前項目の続き)
	DescriptionLength int
	DescriptionChar   []byte
	ItemLength        int
	ItemChar          []byte
}

// ExtendedEvent は拡張形式イベント記述子 (0x4E)
type ExtendedEvent struct {
	DescriptorNumber     int
	LastDescriptorNumber int
	ISO639LanguageCode   []byte
	LengthOfItems        int
	Items                []ExtendedEventItem
	TextChar             []byte
}

// Component はコンポーネント記述子 (0x50)
type Component struct {
	StreamContent      int
	ComponentType      int
	ComponentTag       int
	ISO639LanguageCode []byte
	TextChar           []byte
}

// ContentGenre はコンテント記述子の 1 ジャンル
type ContentGenre struct {
	L1, L2, U1, U2 int
}

// Content はコンテント記述子 (0x54)
type Content struct {
	Contents []ContentGenre
}

// AudioComponent は音声コンポーネント記述子 (0xC4)
type AudioComponent struct {
	StreamContent     int
	ComponentType     int
	ComponentTag      int
	StreamType        int
	SimulcastGroupTag int
	ESMultiLingual    int
	MainComponentFlag int
	QualityIndicator  int
	SamplingRate      int
	// Lang は ISO_639_language_code (3 バイトに満たない場合は残りが 0)
	Lang [3]byte
	// Lang2 は ES_multi_lingual_flag が 1 のときの ISO_639_language_code_2 (それ以外は nil)
	Lang2 *[3]byte
	// ISO639LanguageCode / ISO639LanguageCode2 は Node 版と同じ生のバイト列 (切り詰められている可能性がある)
	ISO639LanguageCode  []byte
	ISO639LanguageCode2 []byte
	TextChar            []byte
}

// Series はシリーズ記述子 (0xD5)
type Series struct {
	SeriesID            int
	RepeatLabel         int
	ProgramPattern      int
	ExpireDateValidFlag int
	ExpireDate          uint16
	EpisodeNumber       int
	LastEpisodeNumber   int
	SeriesNameChar      []byte
}

// EventGroupEvent はイベントグループ記述子の自ネットワーク内イベント
type EventGroupEvent struct {
	ServiceID int
	EventID   int
}

// EventGroupOtherNetworkEvent はイベントグループ記述子の他ネットワークのイベント
type EventGroupOtherNetworkEvent struct {
	OriginalNetworkID int
	TransportStreamID int
	ServiceID         int
	EventID           int
}

// EventGroup はイベントグループ記述子 (0xD6)
type EventGroup struct {
	GroupType  int
	EventCount int
	Events     []EventGroupEvent
	// OtherNetworkEvents は group_type が 4/5 のときのみ非 nil (Node 版では他の場合 undefined)
	OtherNetworkEvents []EventGroupOtherNetworkEvent
	// PrivateDataByte は group_type が 4/5 以外のときの private_data_byte
	PrivateDataByte []byte
}

// LogoTransmission はロゴ伝送記述子 (0xCF)
type LogoTransmission struct {
	LogoTransmissionType int
	// HasLogoID は logo_id フィールドが存在するか (伝送タイプ 1/2)
	HasLogoID      bool
	LogoID         int
	LogoVersion    int
	DownloadDataID int
	LogoChar       []byte
}

// TerrestrialDeliverySystem は地上分配システム記述子 (0xFA)
type TerrestrialDeliverySystem struct {
	AreaCode         int
	GuardInterval    int
	TransmissionMode int
	Frequencies      []int
}

// TSInformationTransmissionType は TS 情報記述子の伝送種別ループ
type TSInformationTransmissionType struct {
	TransmissionTypeInfo int
	ServiceIDs           []int
}

// TSInformation は TS 情報記述子 (0xCD)
type TSInformation struct {
	RemoteControlKeyID    int
	LengthOfTSName        int
	TransmissionTypeCount int
	TSNameChar            []byte
	TransmissionTypes     []TSInformationTransmissionType
}

// StreamIdentifier はストリーム識別記述子 (0x52)
type StreamIdentifier struct {
	ComponentTag int
}

// ConditionalAccess は限定受信方式記述子 (0x09)
type ConditionalAccess struct {
	CASystemID      int
	CAPID           int
	PrivateDataByte []byte
}

// restBytes は記述子末尾までの残りバイトを読む (Node 版 readBytes(2 + descriptor_length - (position >> 3)))
func restBytes(r *reader, length int) []byte {
	return r.readBytes(2 + length - r.bytePos())
}

// DecodeDescriptors は記述子ループをデコードする (Node 版 TsDescriptors#decode)
func DecodeDescriptors(b []byte) []Descriptor {
	ds := []Descriptor{}
	for bytesRead := 0; bytesRead < len(b); {
		tag := int(b[bytesRead])
		bytesRead++
		var raw []byte
		truncated := bytesRead >= len(b)
		if truncated {
			// descriptor_length が欠けている: Node 版では slice(x, NaN) で空バッファとなり、
			// bytesRead が NaN になってループが終了する
			raw = b[:0]
		} else {
			length := int(b[bytesRead])
			bytesRead++
			raw = nodeSlice(b, bytesRead-2, bytesRead+length)
			bytesRead += length
		}
		ds = append(ds, decodeDescriptor(tag, raw))
		if truncated {
			break
		}
	}
	return ds
}

// decodeDescriptor はタグに応じて 1 記述子をデコードする
func decodeDescriptor(tag int, raw []byte) Descriptor {
	if tag == 0x00 || tag == 0x01 {
		return Descriptor{Tag: tag, Raw: raw, Null: true}
	}
	r := &reader{buf: raw}
	d := Descriptor{Raw: raw}
	d.Tag = r.uimsbf(8)
	d.Length = r.uimsbf(8)
	length := d.Length
	switch tag {
	case 0x09:
		v := &ConditionalAccess{}
		v.CASystemID = r.uimsbf(16)
		r.next(3)
		v.CAPID = r.uimsbf(13)
		v.PrivateDataByte = restBytes(r, length)
		d.Value = v
	case 0x48:
		v := &ServiceDescriptor{}
		v.ServiceType = r.uimsbf(8)
		v.ServiceProviderNameChar = r.readBytes(r.uimsbf(8))
		v.ServiceNameChar = r.readBytes(r.uimsbf(8))
		d.Value = v
	case 0x4D:
		v := &ShortEvent{}
		v.ISO639LanguageCode = r.readBytes(3)
		v.EventNameChar = r.readBytes(r.uimsbf(8))
		v.TextChar = r.readBytes(r.uimsbf(8))
		d.Value = v
	case 0x4E:
		v := &ExtendedEvent{Items: []ExtendedEventItem{}}
		v.DescriptorNumber = r.uimsbf(4)
		v.LastDescriptorNumber = r.uimsbf(4)
		v.ISO639LanguageCode = r.readBytes(3)
		v.LengthOfItems = r.uimsbf(8)
		for i := 0; i < v.LengthOfItems; {
			var it ExtendedEventItem
			it.DescriptionLength = r.uimsbf(8)
			it.DescriptionChar = r.readBytes(it.DescriptionLength)
			it.ItemLength = r.uimsbf(8)
			it.ItemChar = r.readBytes(it.ItemLength)
			v.Items = append(v.Items, it)
			i += 2 + it.DescriptionLength + it.ItemLength
		}
		v.TextChar = r.readBytes(r.uimsbf(8))
		d.Value = v
	case 0x50:
		v := &Component{}
		r.next(4)
		v.StreamContent = r.uimsbf(4)
		v.ComponentType = r.uimsbf(8)
		v.ComponentTag = r.uimsbf(8)
		v.ISO639LanguageCode = r.readBytes(3)
		v.TextChar = restBytes(r, length)
		d.Value = v
	case 0x52:
		d.Value = &StreamIdentifier{ComponentTag: r.uimsbf(8)}
	case 0x54:
		v := &Content{Contents: []ContentGenre{}}
		for r.bytePos() < 2+length {
			var g ContentGenre
			g.L1 = r.uimsbf(4)
			g.L2 = r.uimsbf(4)
			g.U1 = r.uimsbf(4)
			g.U2 = r.uimsbf(4)
			v.Contents = append(v.Contents, g)
		}
		d.Value = v
	case 0xC4:
		v := &AudioComponent{}
		r.next(4)
		v.StreamContent = r.uimsbf(4)
		v.ComponentType = r.uimsbf(8)
		v.ComponentTag = r.uimsbf(8)
		v.StreamType = r.uimsbf(8)
		v.SimulcastGroupTag = r.uimsbf(8)
		v.ESMultiLingual = r.uimsbf(1)
		v.MainComponentFlag = r.uimsbf(1)
		v.QualityIndicator = r.uimsbf(2)
		v.SamplingRate = r.uimsbf(3)
		r.next(1)
		v.ISO639LanguageCode = r.readBytes(3)
		copy(v.Lang[:], v.ISO639LanguageCode)
		if v.ESMultiLingual == 1 {
			v.ISO639LanguageCode2 = r.readBytes(3)
			var l2 [3]byte
			copy(l2[:], v.ISO639LanguageCode2)
			v.Lang2 = &l2
		}
		v.TextChar = restBytes(r, length)
		d.Value = v
	case 0xCD:
		v := &TSInformation{TransmissionTypes: []TSInformationTransmissionType{}}
		v.RemoteControlKeyID = r.uimsbf(8)
		v.LengthOfTSName = r.uimsbf(6)
		v.TransmissionTypeCount = r.uimsbf(2)
		v.TSNameChar = r.readBytes(v.LengthOfTSName)
		for range v.TransmissionTypeCount {
			tt := TSInformationTransmissionType{ServiceIDs: []int{}}
			tt.TransmissionTypeInfo = r.uimsbf(8)
			n := r.uimsbf(8)
			for range n {
				tt.ServiceIDs = append(tt.ServiceIDs, r.uimsbf(16))
			}
			v.TransmissionTypes = append(v.TransmissionTypes, tt)
		}
		d.Value = v
	case 0xCF:
		v := &LogoTransmission{}
		v.LogoTransmissionType = r.uimsbf(8)
		switch v.LogoTransmissionType {
		case 1:
			r.next(7)
			v.HasLogoID = true
			v.LogoID = r.uimsbf(9)
			r.next(4)
			v.LogoVersion = r.uimsbf(12)
			v.DownloadDataID = r.uimsbf(16)
		case 2:
			r.next(7)
			v.HasLogoID = true
			v.LogoID = r.uimsbf(9)
		case 3:
			v.LogoChar = restBytes(r, length)
		}
		d.Value = v
	case 0xD5:
		v := &Series{}
		v.SeriesID = r.uimsbf(16)
		v.RepeatLabel = r.uimsbf(4)
		v.ProgramPattern = r.uimsbf(3)
		v.ExpireDateValidFlag = r.uimsbf(1)
		v.ExpireDate = uint16(r.uimsbf(16))
		v.EpisodeNumber = r.uimsbf(12)
		v.LastEpisodeNumber = r.uimsbf(12)
		v.SeriesNameChar = restBytes(r, length)
		d.Value = v
	case 0xD6:
		v := &EventGroup{Events: []EventGroupEvent{}}
		v.GroupType = r.uimsbf(4)
		v.EventCount = r.uimsbf(4)
		for range v.EventCount {
			var e EventGroupEvent
			e.ServiceID = r.uimsbf(16)
			e.EventID = r.uimsbf(16)
			v.Events = append(v.Events, e)
		}
		if v.GroupType == 4 || v.GroupType == 5 {
			v.OtherNetworkEvents = []EventGroupOtherNetworkEvent{}
			for r.bytePos() < 2+length {
				var e EventGroupOtherNetworkEvent
				e.OriginalNetworkID = r.uimsbf(16)
				e.TransportStreamID = r.uimsbf(16)
				e.ServiceID = r.uimsbf(16)
				e.EventID = r.uimsbf(16)
				v.OtherNetworkEvents = append(v.OtherNetworkEvents, e)
			}
		} else {
			v.PrivateDataByte = restBytes(r, length)
		}
		d.Value = v
	case 0xFA:
		v := &TerrestrialDeliverySystem{Frequencies: []int{}}
		v.AreaCode = r.uimsbf(12)
		v.GuardInterval = r.uimsbf(2)
		v.TransmissionMode = r.uimsbf(2)
		for r.bytePos() < 2+length {
			v.Frequencies = append(v.Frequencies, r.uimsbf(16))
		}
		d.Value = v
	}
	return d
}
