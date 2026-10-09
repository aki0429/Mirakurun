package aribts

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// TestLogoToPNG は LogoToPNG が Node 版 TsLogo.decode と同じバイト列を返すことを確認する。
func TestLogoToPNG(t *testing.T) {
	got := LogoToPNG(logoPNGInput)
	if !bytes.Equal(got, logoPNGWant) {
		n := min(len(got), len(logoPNGWant))
		diff := -1
		for i := 0; i < n; i++ {
			if got[i] != logoPNGWant[i] {
				diff = i
				break
			}
		}
		t.Fatalf("LogoToPNG 不一致: got=%d bytes want=%d bytes 最初の差分位置=%d", len(got), len(logoPNGWant), diff)
	}
	// CLUT (PLTE) と透過 (tRNS) チャンクが Node と同じ位置・長さで入っていることを確認する
	if !bytes.Contains(got, logoPLTEBuf) || !bytes.Contains(got, logoTRNSBuf) {
		t.Errorf("PLTE/tRNS チャンクが見つからない")
	}
	if !bytes.Equal(got[33:33+len(logoPLTEBuf)], logoPLTEBuf) {
		t.Errorf("PLTE チャンクの位置がオフセット 33 でない")
	}
	if !bytes.Equal(got[33+len(logoPLTEBuf):33+len(logoPLTEBuf)+len(logoTRNSBuf)], logoTRNSBuf) {
		t.Errorf("tRNS チャンクの位置が PLTE の直後でない")
	}
}

// TestDecodeLogoModule はデータカルーセルのロゴモジュールのデコードを確認する
func TestDecodeLogoModule(t *testing.T) {
	got := DecodeLogoModule(logoModuleInput)
	if len(got) != 1 {
		t.Fatalf("ロゴ数 = %d, want 1", len(got))
	}
	if got[0].LogoID != logoModuleWant.LogoID {
		t.Errorf("LogoID = %d, want %d", got[0].LogoID, logoModuleWant.LogoID)
	}
	if len(got[0].Services) != len(logoModuleWant.Services) {
		t.Fatalf("サービス数 = %d, want %d", len(got[0].Services), len(logoModuleWant.Services))
	}
	for i, s := range got[0].Services {
		w := logoModuleWant.Services[i]
		if s.OriginalNetworkID != w[0] || s.TransportStreamID != w[1] || s.ServiceID != w[2] {
			t.Errorf("サービス[%d] = {%d,%d,%d}, want {%d,%d,%d}", i, s.OriginalNetworkID, s.TransportStreamID, s.ServiceID, w[0], w[1], w[2])
		}
	}
	if !bytes.Equal(got[0].Data, logoModuleWant.Data) {
		t.Errorf("ロゴデータ = %x, want %x", got[0].Data, logoModuleWant.Data)
	}
}

// TestDecodeCDTLogoModule は CDT ロゴモジュールのデコードを確認する
func TestDecodeCDTLogoModule(t *testing.T) {
	logoType, logoID, data := DecodeCDTLogoModule(cdtLogoInput)
	if logoType != cdtLogoWant.LogoType || logoID != cdtLogoWant.LogoID || !bytes.Equal(data, cdtLogoWant.Data) {
		t.Errorf("DecodeCDTLogoModule = (%d,%d,%x), want (%d,%d,%x)", logoType, logoID, data, cdtLogoWant.LogoType, cdtLogoWant.LogoID, cdtLogoWant.Data)
	}
}
