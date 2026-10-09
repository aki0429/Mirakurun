package tuner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"testing"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/aribts"
	"github.com/aki0429/Mirakurun/go/internal/core"
)

// synthLogoData は LogoToPNG の入力となる合成ロゴデータを作る (PNG シグネチャ + ダミー本体)。
func synthLogoData(size int, seed byte) []byte {
	d := make([]byte, size)
	copy(d, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) // PNG シグネチャ
	for i := 8; i < size; i++ {
		d[i] = byte(int(seed) + i)
	}
	return d
}

// buildLogoModule は data_module/logo.js 形式のロゴモジュールを組み立てる。
func buildLogoModule(logoID int, svc [3]int, data []byte) []byte {
	var b []byte
	b = append(b, 0x01)                                      // logo_type
	b = append(b, 0x00, 0x01)                                // numberOfLoop = 1
	b = append(b, byte((logoID>>8)&0x01), byte(logoID&0xFF)) // 7bit skip + 9bit logo_id
	b = append(b, 0x01)                                      // numberOfServices = 1
	b = append(b,
		byte(svc[0]>>8), byte(svc[0]&0xFF),
		byte(svc[1]>>8), byte(svc[1]&0xFF),
		byte(svc[2]>>8), byte(svc[2]&0xFF),
	)
	b = append(b, byte(len(data)>>8), byte(len(data)&0xFF))
	b = append(b, data...)
	return b
}

// buildCDTLogoModule は data_module/cdt_logo.js 形式の CDT ロゴモジュールを組み立てる。
func buildCDTLogoModule(logoType, logoID, version int, data []byte) []byte {
	var b []byte
	b = append(b, byte(logoType))
	b = append(b, byte((logoID>>8)&0x01), byte(logoID&0xFF), byte((version>>8)&0x0F), byte(version&0xFF))
	b = append(b, byte(len(data)>>8), byte(len(data)&0xFF))
	b = append(b, data...)
	return b
}

// TestTSFilterDSMCCLogoSave は DSM-CC (DII + DDB) 経由のロゴ保存を合成データで検証する。
// 保存された PNG が aribts.LogoToPNG(data) とバイト一致することを確認する
// (LogoToPNG は aribts 側の logo_gen_test で Node 版と一致済み)。
func TestTSFilterDSMCCLogoSave(t *testing.T) {
	const netID, svcID, logoID = 31938, 65484, 291
	channels := []apid.ConfigChannelsItem{{Name: "T13", Type: apid.ChannelTypeGR, Channel: "T13"}}
	services := []apid.Service{{
		ID: 1, ServiceID: svcID, NetworkID: netID, Name: "LOGO",
		Channel: &apid.ServiceChannel{Type: apid.ChannelTypeGR, Channel: "T13"},
	}}
	app, _ := buildApp(t, channels, services, nil)

	tf := NewTSFilter(app, TSFilterOptions{Output: io.Discard})

	// 4066 バイト境界をまたぐよう 5000 バイトのロゴデータにする (2 ブロック)。
	data := synthLogoData(5000, 7)
	module := buildLogoModule(logoID, [3]int{netID, 31937, svcID}, data)

	// DII: モジュール記述子 tag 0x02 / text_char "LOGO-05" を持つ module を 1 つ広告する。
	dii := &aribts.DSMCC{TableID: 0x3B, Message: &aribts.DSMCCDownloadInfoIndication{
		DownloadID: 1,
		Modules: []aribts.DSMCCModule{{
			ModuleID:      1,
			ModuleSize:    len(module),
			ModuleVersion: 1,
			ModuleInfo: []aribts.DSMCCModuleDescriptor{{
				Tag: 0x02, Length: len("LOGO-05"), TextChar: []byte("LOGO-05"),
			}},
		}},
	}}
	tf.onDSMCC(dii)

	// DDB: 4066 バイト単位で分割して送る。
	for i := 0; i*dsmccBlockSize < len(module); i++ {
		start := i * dsmccBlockSize
		end := start + dsmccBlockSize
		if end > len(module) {
			end = len(module)
		}
		ddb := &aribts.DSMCC{TableID: 0x3C, Message: &aribts.DSMCCDownloadDataBlock{
			DownloadID: 1, ModuleID: 1, ModuleVersion: 1, BlockNumber: i, BlockDataByte: module[start:end],
		}}
		tf.onDSMCC(ddb)
	}

	// サービスに logoId が設定され、PNG が保存されていること。
	svc := app.Service.GetByNetworkID(netID, svcID)
	if svc == nil {
		t.Fatal("service not found in store")
	}
	if svc.LogoID() == nil || *svc.LogoID() != logoID {
		t.Fatalf("service logoId = %v, want %d", svc.LogoID(), logoID)
	}

	want := aribts.LogoToPNG(data)
	path := core.GetLogoDataPath(app.Config.LogoDataDirPath, netID, logoID)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("logo file not written: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("DSM-CC logo PNG mismatch: got %d bytes, want %d bytes", len(got), len(want))
	}
	gh := sha256.Sum256(got)
	wh := sha256.Sum256(want)
	t.Logf("DSMCC LOGO saved=%s sha256=%s (want sha256=%s, %d bytes)", path, hex.EncodeToString(gh[:]), hex.EncodeToString(wh[:]), len(got))
}

// TestTSFilterCDTLogoSave は CDT 経由のロゴ保存を合成データで検証する (logo_type 0x05)。
func TestTSFilterCDTLogoSave(t *testing.T) {
	const netID, logoID = 31938, 17
	app, _ := buildApp(t, nil, nil, nil)
	tf := NewTSFilter(app, TSFilterOptions{Output: io.Discard})

	data := synthLogoData(64, 30)
	module := buildCDTLogoModule(0x05, logoID, 1110, data)

	tf.onCDT(&aribts.CDT{
		TableID: 0xC8, DataType: 0x01, OriginalNetworkID: netID, DataModuleByte: module,
	})

	want := aribts.LogoToPNG(data)
	path := core.GetLogoDataPath(app.Config.LogoDataDirPath, netID, logoID)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("CDT logo file not written: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("CDT logo PNG mismatch: got %d bytes, want %d bytes", len(got), len(want))
	}
	gh := sha256.Sum256(got)
	t.Logf("CDT LOGO saved=%s sha256=%s (%d bytes)", path, hex.EncodeToString(gh[:]), len(got))

	// logo_type が 0x05 以外なら保存しない。
	app2, _ := buildApp(t, nil, nil, nil)
	tf2 := NewTSFilter(app2, TSFilterOptions{Output: io.Discard})
	tf2.onCDT(&aribts.CDT{
		TableID: 0xC8, DataType: 0x01, OriginalNetworkID: netID,
		DataModuleByte: buildCDTLogoModule(0x03, logoID, 1110, data),
	})
	if _, err := os.Stat(core.GetLogoDataPath(app2.Config.LogoDataDirPath, netID, logoID)); err == nil {
		t.Fatalf("logo_type != 0x05 must not be saved")
	}
}
