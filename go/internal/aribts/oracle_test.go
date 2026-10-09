package aribts

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// oracleLine は Node オラクル (oracle.js) が出力する 1 行
type oracleLine struct {
	Pid        int             `json:"pid"`
	Table      string          `json:"table"`
	SectionHex string          `json:"section_hex"`
	D          json.RawMessage `json:"d"`
}

// tableForSectionID は table_id から Node 版 stream_lite と同じ分類でテーブル名を返す
func tableForSectionID(tid int) string {
	switch {
	case tid == 0x00:
		return "pat"
	case tid == 0x02:
		return "pmt"
	case tid >= 0x3A && tid <= 0x3F:
		return "dsmcc"
	case tid == 0x40 || tid == 0x41:
		return "nit"
	case tid == 0x42 || tid == 0x46:
		return "sdt"
	case tid >= 0x4E && tid <= 0x6F:
		return "eit"
	case tid == 0x73:
		return "tot"
	case tid == 0xC8:
		return "cdt"
	}
	return ""
}

// tagList は記述子タグ列を返す。Node 版が undefined を積む 0x00/0x01 (Null) は -1 にする。
func tagList(descs []Descriptor) []any {
	out := make([]any, 0, len(descs))
	for _, d := range descs {
		if d.Null {
			out = append(out, -1)
		} else {
			out = append(out, d.Tag)
		}
	}
	return out
}

// extractChars は EIT/SDT の記述子から文字列を Node 版と同じ順序で取り出す
func extractChars(descs []Descriptor) []any {
	out := []any{}
	for _, d := range descs {
		if d.Null || d.Value == nil {
			continue
		}
		switch d.Tag {
		case 0x48:
			v := d.Value.(*ServiceDescriptor)
			out = append(out, DecodeString(v.ServiceProviderNameChar), DecodeString(v.ServiceNameChar))
		case 0x4D:
			v := d.Value.(*ShortEvent)
			out = append(out, DecodeString(v.EventNameChar), DecodeString(v.TextChar))
		case 0x4E:
			v := d.Value.(*ExtendedEvent)
			for _, it := range v.Items {
				out = append(out, DecodeString(it.DescriptionChar), DecodeString(it.ItemChar))
			}
			out = append(out, DecodeString(v.TextChar))
		case 0x50:
			out = append(out, DecodeString(d.Value.(*Component).TextChar))
		case 0xC4:
			out = append(out, DecodeString(d.Value.(*AudioComponent).TextChar))
		case 0xCD:
			out = append(out, DecodeString(d.Value.(*TSInformation).TSNameChar))
		case 0xD5:
			out = append(out, DecodeString(d.Value.(*Series).SeriesNameChar))
		case 0xCF:
			if v := d.Value.(*LogoTransmission); v.LogoTransmissionType == 3 {
				out = append(out, DecodeString(v.LogoChar))
			}
		}
	}
	return out
}

// canonicalFor は Go のデコーダ結果を Node オラクルと同じ形のオブジェクトへ変換する。
// ok=false は CRC 不一致で Node 版も出力しないケース。
func canonicalFor(table string, sec []byte) (map[string]any, bool) {
	switch table {
	case "pat":
		o, err := DecodePAT(sec)
		if err != nil {
			return nil, false
		}
		programs := []any{}
		for _, p := range o.Programs {
			programs = append(programs, map[string]any{"program_number": p.ProgramNumber, "network_pid": p.NetworkPID, "program_map_pid": p.ProgramMapPID})
		}
		return map[string]any{"table_id": o.TableID, "section_syntax_indicator": o.SectionSyntaxIndicator, "section_length": o.SectionLength, "transport_stream_id": o.TransportStreamID, "version_number": o.VersionNumber, "current_next_indicator": o.CurrentNextIndicator, "section_number": o.SectionNumber, "last_section_number": o.LastSectionNumber, "programs": programs}, true
	case "pmt":
		o, err := DecodePMT(sec)
		if err != nil {
			return nil, false
		}
		streams := []any{}
		for _, s := range o.Streams {
			streams = append(streams, map[string]any{"stream_type": s.StreamType, "elementary_pid": s.ElementaryPID, "es_info_length": s.ESInfoLength, "es_info_tags": tagList(s.ESInfo)})
		}
		return map[string]any{"table_id": o.TableID, "section_length": o.SectionLength, "program_number": o.ProgramNumber, "version_number": o.VersionNumber, "current_next_indicator": o.CurrentNextIndicator, "section_number": o.SectionNumber, "last_section_number": o.LastSectionNumber, "pcr_pid": o.PCRPID, "program_info_length": o.ProgramInfoLength, "program_info_tags": tagList(o.ProgramInfo), "streams": streams}, true
	case "nit":
		o, err := DecodeNIT(sec)
		if err != nil {
			return nil, false
		}
		tss := []any{}
		for _, t := range o.TransportStreams {
			tss = append(tss, map[string]any{"transport_stream_id": t.TransportStreamID, "original_network_id": t.OriginalNetworkID, "descriptors_tags": tagList(t.Descriptors)})
		}
		return map[string]any{"table_id": o.TableID, "section_length": o.SectionLength, "network_id": o.NetworkID, "version_number": o.VersionNumber, "current_next_indicator": o.CurrentNextIndicator, "section_number": o.SectionNumber, "last_section_number": o.LastSectionNumber, "network_descriptors_tags": tagList(o.NetworkDescriptors), "transport_streams": tss}, true
	case "sdt":
		o, err := DecodeSDT(sec)
		if err != nil {
			return nil, false
		}
		services := []any{}
		for _, s := range o.Services {
			services = append(services, map[string]any{"service_id": s.ServiceID, "EIT_user_defined_flags": s.EITUserDefinedFlags, "EIT_schedule_flag": s.EITScheduleFlag, "EIT_present_following_flag": s.EITPresentFollowingFlag, "running_status": s.RunningStatus, "free_CA_mode": s.FreeCAMode, "descriptors_tags": tagList(s.Descriptors), "chars": extractChars(s.Descriptors)})
		}
		return map[string]any{"table_id": o.TableID, "section_length": o.SectionLength, "transport_stream_id": o.TransportStreamID, "version_number": o.VersionNumber, "current_next_indicator": o.CurrentNextIndicator, "section_number": o.SectionNumber, "last_section_number": o.LastSectionNumber, "original_network_id": o.OriginalNetworkID, "services": services}, true
	case "eit":
		o, err := DecodeEIT(sec)
		if err != nil {
			return nil, false
		}
		events := []any{}
		for _, e := range o.Events {
			events = append(events, map[string]any{"event_id": e.EventID, "start_time": hex.EncodeToString(e.StartTime[:]), "duration": hex.EncodeToString(e.Duration[:]), "running_status": e.RunningStatus, "free_CA_mode": e.FreeCAMode, "descriptors_loop_length": e.DescriptorsLoopLength, "descriptors_tags": tagList(e.Descriptors), "chars": extractChars(e.Descriptors)})
		}
		return map[string]any{"table_id": o.TableID, "section_syntax_indicator": o.SectionSyntaxIndicator, "section_length": o.SectionLength, "service_id": o.ServiceID, "version_number": o.VersionNumber, "current_next_indicator": o.CurrentNextIndicator, "section_number": o.SectionNumber, "last_section_number": o.LastSectionNumber, "transport_stream_id": o.TransportStreamID, "original_network_id": o.OriginalNetworkID, "segment_last_section_number": o.SegmentLastSectionNumber, "last_table_id": o.LastTableID, "events": events}, true
	case "tot":
		o, err := DecodeTOT(sec)
		if err != nil {
			return nil, false
		}
		return map[string]any{"table_id": o.TableID, "section_syntax_indicator": o.SectionSyntaxIndicator, "section_length": o.SectionLength, "JST_time": hex.EncodeToString(o.JSTTime[:]), "descriptors_loop_length": o.DescriptorsLoopLength, "descriptors_tags": tagList(o.Descriptors)}, true
	case "cdt":
		o, err := DecodeCDT(sec)
		if err != nil {
			return nil, false
		}
		return map[string]any{"table_id": o.TableID, "section_syntax_indicator": o.SectionSyntaxIndicator, "section_length": o.SectionLength, "download_data_id": o.DownloadDataID, "version_number": o.VersionNumber, "current_next_indicator": o.CurrentNextIndicator, "section_number": o.SectionNumber, "last_section_number": o.LastSectionNumber, "original_network_id": o.OriginalNetworkID, "data_type": o.DataType, "descriptors_loop_length": o.DescriptorsLoopLength, "descriptors_tags": tagList(o.Descriptors), "data_module_byte": hex.EncodeToString(o.DataModuleByte)}, true
	case "dsmcc":
		o, err := DecodeDSMCC(sec)
		if err != nil {
			return nil, false
		}
		m := map[string]any{"table_id": o.TableID, "section_syntax_indicator": o.SectionSyntaxIndicator, "private_indicator": o.PrivateIndicator, "dsmcc_section_length": o.DSMCCSectionLength, "table_id_extension": o.TableIDExtension, "version_number": o.VersionNumber, "current_next_indicator": o.CurrentNextIndicator, "section_number": o.SectionNumber, "last_section_number": o.LastSectionNumber}
		switch o.TableID {
		case 0x3B:
			dii := o.Message.(*DSMCCDownloadInfoIndication)
			modules := []any{}
			for _, mm := range dii.Modules {
				tags := []any{}
				for _, x := range mm.ModuleInfo {
					tags = append(tags, x.Tag)
				}
				modules = append(modules, map[string]any{"module_id": mm.ModuleID, "module_size": mm.ModuleSize, "module_version": mm.ModuleVersion, "module_info_length": mm.ModuleInfoLength, "module_info_tags": tags})
			}
			m["dii"] = map[string]any{"download_id": dii.DownloadID, "block_size": dii.BlockSize, "number_of_modules": dii.NumberOfModules, "modules": modules, "private_data_length": dii.PrivateDataLength}
		case 0x3C:
			ddb := o.Message.(*DSMCCDownloadDataBlock)
			m["ddb"] = map[string]any{"download_id": ddb.DownloadID, "module_id": ddb.ModuleID, "module_version": ddb.ModuleVersion, "block_number": ddb.BlockNumber, "block_data": hex.EncodeToString(ddb.BlockDataByte)}
		case 0x3D:
			m["stream_descriptor"] = hex.EncodeToString(o.StreamDescriptor)
		case 0x3E:
			m["private_data_byte"] = hex.EncodeToString(o.PrivateDataByte)
		}
		return m, true
	}
	return nil, false
}

func typeName(v any) string {
	if v == nil {
		return "null"
	}
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case float64:
		return "number"
	}
	return fmt.Sprintf("%T", v)
}

// compareJSON は Node オラクルと Go の正規化オブジェクトを数値許容で比較し、差分のパスを返す
func compareJSON(a, b any, path string) string {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			return path + ": 型不一致 (object vs " + typeName(b) + ")"
		}
		keys := map[string]bool{}
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			_, aok := av[k]
			_, bok := bv[k]
			if !aok {
				return path + "." + k + ": Go 側に無い"
			}
			if !bok {
				return path + "." + k + ": Node 側に無い"
			}
			if d := compareJSON(av[k], bv[k], path+"."+k); d != "" {
				return d
			}
		}
		return ""
	case []any:
		bv, ok := b.([]any)
		if !ok {
			return path + ": 型不一致 (array)"
		}
		if len(av) != len(bv) {
			return fmt.Sprintf("%s: 要素数 %d vs %d", path, len(av), len(bv))
		}
		for i := range av {
			if d := compareJSON(av[i], bv[i], fmt.Sprintf("%s[%d]", path, i)); d != "" {
				return d
			}
		}
		return ""
	case string:
		bv, ok := b.(string)
		if !ok || av != bv {
			return fmt.Sprintf("%s: %q vs %v", path, av, b)
		}
		return ""
	case float64:
		bv, ok := b.(float64)
		if !ok || av != bv {
			return fmt.Sprintf("%s: %v vs %v", path, av, b)
		}
		return ""
	case nil:
		if b != nil {
			return path + ": null vs " + typeName(b)
		}
		return ""
	}
	return fmt.Sprintf("%s: 未対応型 %s", path, typeName(a))
}

// TestOracleParity は Node オラクル出力と Go 実装の差分を比較する (環境変数で有効化)。
func TestOracleParity(t *testing.T) {
	oraclePath := os.Getenv("ARIBTS_ORACLE")
	fixture := os.Getenv("ARIBTS_FIXTURE")
	if oraclePath == "" || fixture == "" {
		t.Skip("ARIBTS_ORACLE と ARIBTS_FIXTURE を設定するとオラクル差分比較を実行する")
	}
	rawLines, err := os.ReadFile(oraclePath)
	if err != nil {
		t.Fatalf("オラクル読み込み失敗: %v", err)
	}
	var lines []oracleLine
	for _, ln := range strings.Split(string(rawLines), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var ol oracleLine
		if err := json.Unmarshal([]byte(ln), &ol); err != nil {
			t.Fatalf("オラクル行のパース失敗: %v (%s)", err, ln[:min(len(ln), 80)])
		}
		lines = append(lines, ol)
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("フィクスチャ読み込み失敗: %v", err)
	}

	asm := NewSectionAssembler()
	totals := map[string]int{}
	var mismatches []string
	lineNo := 0
	matched := 0
	for off := 0; off+188 <= len(data); off += 188 {
		pid, secs := asm.Push(data[off : off+188])
		for _, sec := range secs {
			if len(sec) == 0 {
				continue
			}
			table := tableForSectionID(int(sec[0]))
			if table == "" {
				continue
			}
			canon, ok := canonicalFor(table, sec)
			if !ok {
				continue
			}
			totals[table]++
			secHex := hex.EncodeToString(sec)
			if lineNo >= len(lines) {
				mismatches = append(mismatches, fmt.Sprintf("余分なセクション: %s pid=%d hex=%s", table, pid, secHex))
				continue
			}
			ol := lines[lineNo]
			if ol.Pid != int(pid) || ol.Table != table || ol.SectionHex != secHex {
				mismatches = append(mismatches, fmt.Sprintf("#%d セクション不一致: go=(%s,pid=%d) node=(%s,pid=%d) goHex=%s nodeHex=%s", lineNo, table, pid, ol.Table, ol.Pid, secHex, ol.SectionHex))
				lineNo++
				continue
			}
			var nd, gd any
			if err := json.Unmarshal(ol.D, &nd); err != nil {
				t.Fatalf("オラクル d のパース失敗 (#%d): %v", lineNo, err)
			}
			b, err := json.Marshal(canon)
			if err != nil {
				t.Fatalf("Go 正規化の JSON 化失敗 (#%d): %v", lineNo, err)
			}
			if err := json.Unmarshal(b, &gd); err != nil {
				t.Fatalf("Go 正規化のパース失敗 (#%d): %v", lineNo, err)
			}
			if diff := compareJSON(nd, gd, ""); diff != "" {
				mismatches = append(mismatches, fmt.Sprintf("#%d %s pid=%d フィールド不一致: %s (hex=%s)", lineNo, table, pid, diff, ol.SectionHex))
			} else {
				matched++
			}
			lineNo++
		}
	}
	for ; lineNo < len(lines); lineNo++ {
		mismatches = append(mismatches, fmt.Sprintf("Node のみのセクション: %s pid=%d hex=%s", lines[lineNo].Table, lines[lineNo].Pid, lines[lineNo].SectionHex))
	}

	t.Logf("オラクル比較: 一致 %d / オラクル %d 行 / Go 出力 %d", matched, len(lines), matched+countExtra(mismatches))
	for _, tbl := range []string{"pat", "pmt", "nit", "sdt", "eit", "tot", "cdt", "dsmcc"} {
		t.Logf("  %-6s: %d", tbl, totals[tbl])
	}
	if len(mismatches) > 0 {
		for i, m := range mismatches {
			if i >= 50 {
				t.Logf("  ... (残り %d 件省略)", len(mismatches)-i)
				break
			}
			t.Logf("  不一致: %s", m)
		}
		t.Fatalf("オラクル差分: %d 件 (一致 %d)", len(mismatches), matched)
	}
	// 全 Go 出力がオラクルと一致したことを確認
	total := 0
	for _, v := range totals {
		total += v
	}
	if matched != len(lines) || total != len(lines) {
		t.Fatalf("件数不一致: matched=%d lines=%d total=%d", matched, len(lines), total)
	}
}

func countExtra(mismatches []string) int {
	n := 0
	for _, m := range mismatches {
		if strings.Contains(m, "余分なセクション") {
			n++
		}
	}
	return n
}

// BenchmarkSectionAssemblerPush は sample1.ts 相当の TS を Push する速度を測る (ARIBTS_FIXTURE で有効化)
func BenchmarkSectionAssemblerPush(b *testing.B) {
	fixture := os.Getenv("ARIBTS_FIXTURE")
	if fixture == "" {
		b.Skip("ARIBTS_FIXTURE を設定するとベンチマークを実行する")
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		b.Fatalf("フィクスチャ読み込み失敗: %v", err)
	}
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		asm := NewSectionAssembler()
		for off := 0; off+188 <= len(data); off += 188 {
			asm.Push(data[off : off+188])
		}
	}
}
