package aribts

import (
	"encoding/binary"
	"hash/crc32"
)

// LogoService はロゴが対応するサービス
type LogoService struct {
	OriginalNetworkID int
	TransportStreamID int
	ServiceID         int
}

// LogoEntry はロゴデータモジュール内の 1 ロゴ (Node 版 TsDataModuleLogo の logos[] 要素)
type LogoEntry struct {
	LogoID   int
	Services []LogoService
	Data     []byte
}

// DecodeCDTLogoModule は CDT (table_id 0xC8) の data_module_byte をロゴモジュールとしてデコードする
// (Node 版 data_module/cdt_logo.js)。data はロゴの PNG バイト列。
func DecodeCDTLogoModule(dataModuleByte []byte) (logoType int, logoID int, data []byte) {
	r := &reader{buf: dataModuleByte}
	logoType = r.uimsbf(8)
	r.next(7)
	logoID = r.uimsbf(9)
	r.next(4)
	r.uimsbf(12) // logo_version (未使用)
	dataSize := r.uimsbf(16)
	data = r.readBytes(dataSize)
	return logoType, logoID, data
}

// DecodeLogoModule はデータカルーセルのロゴモジュールをデコードする (Node 版 data_module/logo.js)
func DecodeLogoModule(b []byte) []LogoEntry {
	r := &reader{buf: b}
	r.uimsbf(8) // logo_type (未使用)
	numberOfLoop := r.uimsbf(16)
	logos := []LogoEntry{}
	for range numberOfLoop {
		var entry LogoEntry
		r.next(7)
		entry.LogoID = r.uimsbf(9)
		numberOfServices := r.uimsbf(8)
		entry.Services = []LogoService{}
		for range numberOfServices {
			var svc LogoService
			svc.OriginalNetworkID = r.uimsbf(16)
			svc.TransportStreamID = r.uimsbf(16)
			svc.ServiceID = r.uimsbf(16)
			entry.Services = append(entry.Services, svc)
		}
		dataSize := r.uimsbf(16)
		entry.Data = r.readBytes(dataSize)
		logos = append(logos, entry)
	}
	return logos
}

// pngChunk は PNG チャンク (長さ 4 バイト + タイプ 4 バイト + データ + CRC 4 バイト) を生成する
func pngChunk(chunkType string, data []byte) []byte {
	buf := make([]byte, 4+4+len(data)+4)
	binary.BigEndian.PutUint32(buf[0:4], uint32(len(data)))
	copy(buf[4:8], chunkType)
	copy(buf[8:8+len(data)], data)
	// PNG の CRC は「タイプ + データ」に対して計算する (Node 版 logo.js と同じ)
	crc := crc32.ChecksumIEEE(buf[4 : 8+len(data)])
	binary.BigEndian.PutUint32(buf[8+len(data):], crc)
	return buf
}

// logoPLTEBuf は Node 版 logo.js の PLTE_BUF に相当する固定チャンク
var logoPLTEBuf = func() []byte {
	palette := make([]byte, 0, len(logoClut)*3)
	for _, p := range logoClut {
		palette = append(palette, p[0], p[1], p[2])
	}
	return pngChunk("PLTE", palette)
}()

// logoTRNSBuf は Node 版 logo.js の TRNS_BUF に相当する固定チャンク
var logoTRNSBuf = func() []byte {
	alpha := make([]byte, 0, len(logoClut))
	for _, p := range logoClut {
		alpha = append(alpha, p[3])
	}
	return pngChunk("tRNS", alpha)
}()

// LogoToPNG は ARIB ロゴデータを PNG に変換する (Node 版 TsLogo.decode)。
// 入力の先頭 33 バイト (PNG シグネチャ + IHDR) の直後に、ARIB の色テーブル (PLTE + tRNS) を
// 挿入したバイト列を返す。Node 版と 1 バイト単位で一致することを目的としている。
func LogoToPNG(data []byte) []byte {
	head := nodeSlice(data, 0, 33)
	tail := nodeSlice(data, 33, len(data))
	out := make([]byte, 0, len(head)+len(logoPLTEBuf)+len(logoTRNSBuf)+len(tail))
	out = append(out, head...)
	out = append(out, logoPLTEBuf...)
	out = append(out, logoTRNSBuf...)
	out = append(out, tail...)
	return out
}
