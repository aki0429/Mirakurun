package aribts

import "errors"

// ErrCRC は CRC32 が一致しないセクションを表す (Node 版 decode() が null を返すケースに相当)
var ErrCRC = errors.New("aribts: section CRC32 mismatch")

// PATProgram は PAT の 1 プログラム分のエントリ
type PATProgram struct {
	// ProgramNumber は program_number (0 の場合は NIT を指す)
	ProgramNumber int
	// NetworkPID は program_number が 0 のときの network_PID (それ以外では 0)
	NetworkPID int
	// ProgramMapPID は program_number が 0 以外のときの program_map_PID (それ以外では 0)
	ProgramMapPID int
}

// PAT は Program Association Table (table_id 0x00) のデコード結果
type PAT struct {
	TableID                int
	SectionSyntaxIndicator int
	SectionLength          int
	TransportStreamID      int
	VersionNumber          int
	CurrentNextIndicator   int
	SectionNumber          int
	LastSectionNumber      int
	Programs               []PATProgram
	// Raw はセクション全体のバイト列 (Node 版 _raw、入力スライスをそのまま参照する)
	Raw []byte
}

// DecodePAT は PAT セクションをデコードする (Node 版 TsTablePat#decode)。CRC 不一致なら ErrCRC を返す。
func DecodePAT(sec []byte) (*PAT, error) {
	if CRC32(sec) != 0 {
		return nil, ErrCRC
	}
	r := &reader{buf: sec}
	pat := &PAT{Raw: sec}
	pat.TableID = r.uimsbf(8)
	pat.SectionSyntaxIndicator = r.uimsbf(1)
	r.next(3)
	pat.SectionLength = r.uimsbf(12)
	pat.TransportStreamID = r.uimsbf(16)
	r.next(2)
	pat.VersionNumber = r.uimsbf(5)
	pat.CurrentNextIndicator = r.uimsbf(1)
	pat.SectionNumber = r.uimsbf(8)
	pat.LastSectionNumber = r.uimsbf(8)
	pat.Programs = []PATProgram{}
	// CRC_32 の手前までプログラムのループを読む
	for r.bytePos() < 3+pat.SectionLength-4 {
		var p PATProgram
		p.ProgramNumber = r.uimsbf(16)
		r.next(3)
		if p.ProgramNumber == 0 {
			p.NetworkPID = r.uimsbf(13)
		} else {
			p.ProgramMapPID = r.uimsbf(13)
		}
		pat.Programs = append(pat.Programs, p)
	}
	return pat, nil
}
