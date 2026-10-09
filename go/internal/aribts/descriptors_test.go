package aribts

import (
	"bytes"
	"testing"
)

func TestDecodeDescriptorsTyped(t *testing.T) {
	var b []byte
	// 0x48 service: type 0x01, provider "AB", name "XYZ"
	b = append(b, 0x48, 8, 0x01, 2, 'A', 'B', 3, 'X', 'Y', 'Z')
	// 0x4D short_event: "jpn", name 2 bytes, text 1 byte
	b = append(b, 0x4D, 8, 'j', 'p', 'n', 2, 0x41, 0x42, 1, 0x43)
	// 0x52 stream_identifier
	b = append(b, 0x52, 1, 0x79)
	// 0x09 CA: system 0x0005, PID 0x0901, private 2 bytes
	b = append(b, 0x09, 6, 0x00, 0x05, 0xE9, 0x01, 0xAA, 0xBB)
	// 0x00 は Node 版では undefined (Null)
	b = append(b, 0x00, 0)
	// 未対応タグ
	b = append(b, 0x99, 2, 1, 2)
	ds := DecodeDescriptors(b)
	if len(ds) != 6 {
		t.Fatalf("n=%d", len(ds))
	}
	sv, ok := ds[0].Value.(*ServiceDescriptor)
	if !ok || ds[0].Tag != 0x48 || sv.ServiceType != 1 || string(sv.ServiceProviderNameChar) != "AB" || string(sv.ServiceNameChar) != "XYZ" {
		t.Fatalf("service: %+v %+v", ds[0], sv)
	}
	se, ok := ds[1].Value.(*ShortEvent)
	if !ok || string(se.ISO639LanguageCode) != "jpn" || string(se.EventNameChar) != "AB" || string(se.TextChar) != "C" {
		t.Fatalf("short: %+v", se)
	}
	if si, ok := ds[2].Value.(*StreamIdentifier); !ok || si.ComponentTag != 0x79 {
		t.Fatalf("stream id: %+v", ds[2])
	}
	if ca, ok := ds[3].Value.(*ConditionalAccess); !ok || ca.CASystemID != 5 || ca.CAPID != 0x901 || !bytes.Equal(ca.PrivateDataByte, []byte{0xAA, 0xBB}) {
		t.Fatalf("ca: %+v", ds[3].Value)
	}
	if !ds[4].Null || ds[4].Value != nil {
		t.Fatalf("tag 0 must be null: %+v", ds[4])
	}
	if ds[5].Null || ds[5].Value != nil || !bytes.Equal(ds[5].Raw, []byte{0x99, 2, 1, 2}) {
		t.Fatalf("unknown: %+v", ds[5])
	}
}

func TestDecodeDescriptorsEITTypes(t *testing.T) {
	var b []byte
	// 0x4E extended_event: num 0 last 1, "jpn", items: [desc "D" item "II"], text ""
	b = append(b, 0x4E, 11, 0x01, 'j', 'p', 'n', 5, 1, 'D', 2, 'I', 'I', 0)
	// 0x50 component: stream_content 1, type 0xB3, tag 0, "jpn", text "T"
	b = append(b, 0x50, 7, 0xF1, 0xB3, 0x00, 'j', 'p', 'n', 'T')
	// 0x54 content: 2 entries
	b = append(b, 0x54, 4, 0x01, 0x23, 0x45, 0x67)
	// 0xC4 audio_component: multi lingual
	b = append(b, 0xC4, 15, 0xF2, 0x03, 0x10, 0x0F, 0xFF, 0xEF, 'j', 'p', 'n', 'e', 'n', 'g', 'a', 'u', 'd')
	// 0xD5 series: id 0x1234, repeat 1, pattern 2, valid 1, expire 0xC079, ep 5, last 12, name "S"
	b = append(b, 0xD5, 9, 0x12, 0x34, 0x15, 0xC0, 0x79, 0x00, 0x50, 0x0C, 'S')
	// 0xD6 event_group: type 1, 1 event, private 1 byte
	b = append(b, 0xD6, 6, 0x11, 0x00, 0x10, 0x00, 0x20, 0x99)
	// 0xD6 event_group: type 4, 0 events, 1 other network event
	b = append(b, 0xD6, 9, 0x40, 0x00, 0x04, 0x40, 0x01, 0x00, 0x65, 0x12, 0x34)
	ds := DecodeDescriptors(b)
	if len(ds) != 7 {
		t.Fatalf("n=%d", len(ds))
	}
	ee := ds[0].Value.(*ExtendedEvent)
	if ee.DescriptorNumber != 0 || ee.LastDescriptorNumber != 1 || len(ee.Items) != 1 ||
		string(ee.Items[0].DescriptionChar) != "D" || string(ee.Items[0].ItemChar) != "II" || ee.Items[0].DescriptionLength != 1 {
		t.Fatalf("extended: %+v", ee)
	}
	if c := ds[1].Value.(*Component); c.StreamContent != 1 || c.ComponentType != 0xB3 || string(c.TextChar) != "T" {
		t.Fatalf("component: %+v", c)
	}
	ct := ds[2].Value.(*Content)
	if len(ct.Contents) != 2 || ct.Contents[0] != (ContentGenre{0, 1, 2, 3}) || ct.Contents[1] != (ContentGenre{4, 5, 6, 7}) {
		t.Fatalf("content: %+v", ct)
	}
	ac := ds[3].Value.(*AudioComponent)
	if ac.StreamContent != 2 || ac.ComponentType != 3 || ac.ComponentTag != 0x10 || ac.MainComponentFlag != 1 || ac.SamplingRate != 7 ||
		ac.Lang != [3]byte{'j', 'p', 'n'} || ac.Lang2 == nil || *ac.Lang2 != [3]byte{'e', 'n', 'g'} || string(ac.TextChar) != "aud" {
		t.Fatalf("audio: %+v", ac)
	}
	sr := ds[4].Value.(*Series)
	if sr.SeriesID != 0x1234 || sr.RepeatLabel != 1 || sr.ProgramPattern != 2 || sr.ExpireDateValidFlag != 1 || sr.ExpireDate != 0xC079 ||
		sr.EpisodeNumber != 5 || sr.LastEpisodeNumber != 12 || string(sr.SeriesNameChar) != "S" {
		t.Fatalf("series: %+v", sr)
	}
	eg := ds[5].Value.(*EventGroup)
	if eg.GroupType != 1 || len(eg.Events) != 1 || eg.Events[0] != (EventGroupEvent{ServiceID: 0x10, EventID: 0x20}) || eg.OtherNetworkEvents != nil {
		t.Fatalf("group1: %+v", eg)
	}
	eg = ds[6].Value.(*EventGroup)
	if eg.GroupType != 4 || len(eg.Events) != 0 || len(eg.OtherNetworkEvents) != 1 ||
		eg.OtherNetworkEvents[0] != (EventGroupOtherNetworkEvent{OriginalNetworkID: 4, TransportStreamID: 0x4001, ServiceID: 0x65, EventID: 0x1234}) {
		t.Fatalf("group4: %+v", eg)
	}
}

func TestDecodeDescriptorsNetworkTypes(t *testing.T) {
	var b []byte
	// 0xCF logo_transmission type 1 / type 2 / type 3
	b = append(b, 0xCF, 7, 0x01, 0xFE, 0x05, 0xF0, 0x02, 0x00, 0x03)
	b = append(b, 0xCF, 3, 0x02, 0xFF, 0xFF)
	b = append(b, 0xCF, 2, 0x03, 'L')
	// 0xFA terrestrial: area 0x123, guard 2, mode 1, freq 0x1000
	b = append(b, 0xFA, 4, 0x12, 0x39, 0x10, 0x00)
	// 0xCD ts_information: key 3, name len 1, 0 types, name "N"
	b = append(b, 0xCD, 3, 0x03, 0x04, 'N')
	ds := DecodeDescriptors(b)
	if l := ds[0].Value.(*LogoTransmission); l.LogoTransmissionType != 1 || !l.HasLogoID || l.LogoID != 5 || l.LogoVersion != 2 || l.DownloadDataID != 3 {
		t.Fatalf("logo1: %+v", l)
	}
	if l := ds[1].Value.(*LogoTransmission); !l.HasLogoID || l.LogoID != 0x1FF {
		t.Fatalf("logo2: %+v", l)
	}
	if l := ds[2].Value.(*LogoTransmission); l.HasLogoID || string(l.LogoChar) != "L" {
		t.Fatalf("logo3: %+v", l)
	}
	if td := ds[3].Value.(*TerrestrialDeliverySystem); td.AreaCode != 0x123 || td.GuardInterval != 2 || td.TransmissionMode != 1 || len(td.Frequencies) != 1 || td.Frequencies[0] != 0x1000 {
		t.Fatalf("terrestrial: %+v", td)
	}
	if ti := ds[4].Value.(*TSInformation); ti.RemoteControlKeyID != 3 || string(ti.TSNameChar) != "N" {
		t.Fatalf("ts_information: %+v", ti)
	}
}

// TestDecodeDescriptorsTruncated は Node 版と同じく、長さバイトが欠けた末尾のディスクリプタを空バッファでデコードして終了することを確認する
func TestDecodeDescriptorsTruncated(t *testing.T) {
	ds := DecodeDescriptors([]byte{0x52, 1, 7, 0x4D})
	if len(ds) != 2 {
		t.Fatalf("n=%d", len(ds))
	}
	se, ok := ds[1].Value.(*ShortEvent)
	if !ok || ds[1].Tag != 0 || len(ds[1].Raw) != 0 || len(se.EventNameChar) != 0 {
		t.Fatalf("truncated: %+v %+v", ds[1], se)
	}
	// 長さが残りより長い場合は切り詰められたバッファでデコードする
	ds = DecodeDescriptors([]byte{0x52, 5, 9})
	if si := ds[0].Value.(*StreamIdentifier); len(ds) != 1 || si.ComponentTag != 9 || ds[0].Length != 5 {
		t.Fatalf("short buffer: %+v", ds)
	}
}
