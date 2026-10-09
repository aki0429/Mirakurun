package aribts

import (
	"bytes"
	"testing"
)

// buildSection は flagsHighNibble を section ヘッダの上位 4bit に置き、本体 body と正しい CRC_32 を付けた
// セクションを組み立てる (section_length は body+CRC から自動計算)。テスト専用。
func buildSection(tableID byte, flagsHighNibble byte, body []byte) []byte {
	length := len(body) + 4
	sec := []byte{tableID, flagsHighNibble | byte(length>>8), byte(length)}
	sec = append(sec, body...)
	crc := CRC32(sec)
	return append(sec, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

// TestBuildSectionCRC はテストヘルパが正しい CRC を付けることを確認する
func TestBuildSectionCRC(t *testing.T) {
	sec := buildSection(0x73, 0x70, []byte{1, 2, 3})
	if CRC32(sec) != 0 {
		t.Fatalf("buildSection の CRC が 0 でない: %08x", CRC32(sec))
	}
}

// TestDecodeEIT は手組みの EIT セクションをデコードする
func TestDecodeEIT(t *testing.T) {
	// short_event 記述子: lang=jpn, event_name="これに", text="ぁ"
	name := []byte{0x1B, 0x24, 0x42, 0x24, 0x33, 0x24, 0x6C, 0x24, 0x4B}
	text := []byte{0x1B, 0x28, 0x30, 0x21}
	desc := append([]byte{0x4D, byte(3 + 1 + len(name) + 1 + len(text)), 0x6A, 0x70, 0x6E, byte(len(name))}, name...)
	desc = append(desc, byte(len(text)))
	desc = append(desc, text...)

	body := []byte{}
	body = append(body, 0xCD, 0x91)    // service_id = 0xCD91
	body = append(body, 0xC0|(2<<1)|1) // reserved 11, version 2, cni 1
	body = append(body, 0x00, 0x00)    // section_number, last_section_number
	body = append(body, 0x7C, 0xC2)    // transport_stream_id
	body = append(body, 0x7C, 0xC2)    // original_network_id
	body = append(body, 0x01, 0x4E)    // segment_last_section_number, last_table_id
	// イベント 1 個
	body = append(body, 0x00, 0x01)                                 // event_id
	body = append(body, 0xEF, 0x86, 0x17, 0x52, 0x53)               // start_time
	body = append(body, 0x00, 0x30, 0x00)                           // duration
	body = append(body, byte(4<<5)|byte(0<<4)|byte((len(desc))>>8)) // running_status=4, free_CA=0
	body = append(body, byte(len(desc)))
	body = append(body, desc...)

	sec := buildSection(0x4E, 0xF0, body)
	eit, err := DecodeEIT(sec)
	if err != nil {
		t.Fatalf("DecodeEIT 失敗: %v", err)
	}
	if eit.TableID != 0x4E || eit.SectionSyntaxIndicator != 1 || eit.ServiceID != 0xCD91 || eit.VersionNumber != 2 || eit.CurrentNextIndicator != 1 {
		t.Errorf("ヘッダフィールド不一致: %+v", eit)
	}
	if eit.TransportStreamID != 0x7CC2 || eit.OriginalNetworkID != 0x7CC2 || eit.LastTableID != 0x4E {
		t.Errorf("ID フィールド不一致: tsid=%x onid=%x last=%x", eit.TransportStreamID, eit.OriginalNetworkID, eit.LastTableID)
	}
	if len(eit.Events) != 1 {
		t.Fatalf("イベント数 = %d, want 1", len(eit.Events))
	}
	ev := eit.Events[0]
	if ev.EventID != 1 || ev.RunningStatus != 4 || ev.FreeCAMode != 0 {
		t.Errorf("イベントフィールド不一致: %+v", ev)
	}
	if ev.StartTime != [5]byte{0xEF, 0x86, 0x17, 0x52, 0x53} || ev.Duration != [3]byte{0x00, 0x30, 0x00} {
		t.Errorf("時刻フィールド不一致: start=%x dur=%x", ev.StartTime, ev.Duration)
	}
	if len(ev.Descriptors) != 1 || ev.Descriptors[0].Tag != 0x4D {
		t.Fatalf("記述子不一致: %+v", ev.Descriptors)
	}
	se := ev.Descriptors[0].Value.(*ShortEvent)
	if got := DecodeString(se.EventNameChar); got != "これに" {
		t.Errorf("イベント名 = %q, want これに", got)
	}
	if got := DecodeString(se.TextChar); got != "ぁ" {
		t.Errorf("説明 = %q, want ぁ", got)
	}
	// BCD/MJD 変換の確認
	if ev.StartTime[0] != 0xEF {
		t.Errorf("start_time 先頭 = %x", ev.StartTime[0])
	}
	if MJDToUnixMs(ev.StartTime[:]) <= 0 {
		t.Errorf("MJDToUnixMs が非正")
	}
	if BCD24ToMs(ev.Duration[:]) != (0*3600+30*60+0)*1000 {
		t.Errorf("BCD24ToMs = %d", BCD24ToMs(ev.Duration[:]))
	}
}

// TestDecodeEITCRCError は CRC 不一致で ErrCRC を返すことを確認する
func TestDecodeEITCRCError(t *testing.T) {
	sec := buildSection(0x4E, 0xF0, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12})
	sec[2] ^= 0xFF
	if _, err := DecodeEIT(sec); err != ErrCRC {
		t.Fatalf("err = %v, want ErrCRC", err)
	}
}

// TestDecodeTOT は手組みの TOT セクションをデコードする
func TestDecodeTOT(t *testing.T) {
	body := []byte{0xEF, 0x86, 0x17, 0x52, 0x53, 0x00, 0x00} // JST_time + (dll=0)
	sec := buildSection(0x73, 0x70, body)
	tot, err := DecodeTOT(sec)
	if err != nil {
		t.Fatalf("DecodeTOT 失敗: %v", err)
	}
	if tot.TableID != 0x73 || tot.SectionSyntaxIndicator != 0 {
		t.Errorf("ヘッダ不一致: %+v", tot)
	}
	if tot.JSTTime != [5]byte{0xEF, 0x86, 0x17, 0x52, 0x53} {
		t.Errorf("JSTTime = %x", tot.JSTTime)
	}
	if tot.DescriptorsLoopLength != 0 || len(tot.Descriptors) != 0 {
		t.Errorf("記述子不一致: %+v", tot)
	}
}

// TestDecodeCDT は手組みの CDT セクションをデコードする
func TestDecodeCDT(t *testing.T) {
	body := []byte{0x00, 0x01, 0xC3, 0x00, 0x00, 0x7C, 0xC1, 0x01, 0x00, 0x00, 0xAA, 0xBB}
	sec := buildSection(0xC8, 0xF0, body)
	cdt, err := DecodeCDT(sec)
	if err != nil {
		t.Fatalf("DecodeCDT 失敗: %v", err)
	}
	if cdt.TableID != 0xC8 || cdt.DownloadDataID != 1 || cdt.VersionNumber != 1 || cdt.CurrentNextIndicator != 1 {
		t.Errorf("ヘッダ不一致: %+v", cdt)
	}
	if cdt.OriginalNetworkID != 0x7CC1 || cdt.DataType != 1 {
		t.Errorf("ID/type 不一致: onid=%x data_type=%d", cdt.OriginalNetworkID, cdt.DataType)
	}
	if !bytes.Equal(cdt.DataModuleByte, []byte{0xAA, 0xBB}) {
		t.Errorf("DataModuleByte = %x, want aabb", cdt.DataModuleByte)
	}
}

// TestDecodeDSMCCDII は手組みの DII (table_id 0x3B) をデコードする
func TestDecodeDSMCCDII(t *testing.T) {
	dii := []byte{0x11, 0x03, 0x10, 0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x4F, 0xFF, 0xFF, 0xFF, 0x10, 0x03, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	body := []byte{0x07, 0xB4, 0xC1, 0x00, 0x00}
	body = append(body, dii...)
	sec := buildSection(0x3B, 0xB0, body)
	d, err := DecodeDSMCC(sec)
	if err != nil {
		t.Fatalf("DecodeDSMCC 失敗: %v", err)
	}
	if d.TableID != 0x3B || d.SectionSyntaxIndicator != 1 || d.PrivateIndicator != 0 {
		t.Fatalf("ヘッダ不一致: %+v", d)
	}
	msg, ok := d.Message.(*DSMCCDownloadInfoIndication)
	if !ok {
		t.Fatalf("Message 型 = %T", d.Message)
	}
	if msg.DownloadID != 0x4FFFFFFF {
		t.Errorf("DownloadID = %x", msg.DownloadID)
	}
	if msg.BlockSize != 0x1003 || msg.NumberOfModules != 0 {
		t.Errorf("BlockSize=%x NumberOfModules=%d", msg.BlockSize, msg.NumberOfModules)
	}
	if msg.CompatibilityDescriptor == nil {
		t.Errorf("CompatibilityDescriptor が nil")
	}
}

// TestDecodeDSMCCDDB は手組みの DDB (table_id 0x3C) をデコードする
func TestDecodeDSMCCDDB(t *testing.T) {
	ddb := []byte{0x11, 0x03, 0x10, 0x03, 0x4F, 0xFF, 0xFF, 0xFF, 0x00, 0x00, 0x00, 0x0A,
		0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x01, 0x02, 0x03, 0x04}
	body := []byte{0x07, 0xB4, 0xC1, 0x00, 0x00}
	body = append(body, ddb...)
	sec := buildSection(0x3C, 0xB0, body)
	d, err := DecodeDSMCC(sec)
	if err != nil {
		t.Fatalf("DecodeDSMCC 失敗: %v", err)
	}
	msg := d.Message.(*DSMCCDownloadDataBlock)
	if msg.DownloadID != 0x4FFFFFFF || msg.ModuleID != 1 || msg.BlockNumber != 1 {
		t.Errorf("DDB フィールド不一致: %+v", msg)
	}
	if !bytes.Equal(msg.BlockDataByte, []byte{0x01, 0x02, 0x03, 0x04}) {
		t.Errorf("BlockDataByte = %x", msg.BlockDataByte)
	}
}

// TestDecodeDSMCCNoCRC は section_syntax_indicator が 0 のとき CRC 検査をしないことを確認する
func TestDecodeDSMCCNoCRC(t *testing.T) {
	body := []byte{0x07, 0xB4, 0x01, 0x00, 0x00, 0xAA, 0xBB}
	sec := buildSection(0x3E, 0x30, body) // ssi=0
	sec[len(sec)-1] ^= 0xFF               // CRC を壊す (長さは変えない)
	d, err := DecodeDSMCC(sec)
	if err != nil {
		t.Fatalf("ssi=0 では CRC 検査しないはず: %v", err)
	}
	if len(d.Checksum) != 4 {
		t.Errorf("Checksum 長 = %d, want 4", len(d.Checksum))
	}
}

// TestDecodeDSMCCCRCError は ssi=1 で CRC 不一致なら ErrCRC を返すことを確認する
func TestDecodeDSMCCCRCError(t *testing.T) {
	body := []byte{0x07, 0xB4, 0xC1, 0x00, 0x00, 0x11, 0x03, 0x10, 0x03}
	sec := buildSection(0x3B, 0xB0, body)
	sec[2] ^= 0xFF
	if _, err := DecodeDSMCC(sec); err != ErrCRC {
		t.Fatalf("err = %v, want ErrCRC", err)
	}
}
