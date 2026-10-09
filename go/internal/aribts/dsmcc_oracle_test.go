package aribts

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// dsmccOracleLine は ORACLE_DSMCC_DETAIL=1 で出力した JSONL の 1 行。
// Node 版 TsStreamLite が組み立て・デコードした DSM-CC セクションと、
// DII moduleInfo の詳細 (descriptor_tag / descriptor_length / text_char) を保持する。
type dsmccOracleLine struct {
	Pid        int    `json:"pid"`
	Table      string `json:"table"`
	SectionHex string `json:"section_hex"`
	D          struct {
		TableID int `json:"table_id"`
		DII     *struct {
			DownloadID      int `json:"download_id"`
			BlockSize       int `json:"block_size"`
			NumberOfModules int `json:"number_of_modules"`
			Modules         []struct {
				ModuleID         int   `json:"module_id"`
				ModuleSize       int   `json:"module_size"`
				ModuleVersion    int   `json:"module_version"`
				ModuleInfoLength int   `json:"module_info_length"`
				ModuleInfoTags   []int `json:"module_info_tags"`
				ModuleInfo       []struct {
					DescriptorTag    int    `json:"descriptor_tag"`
					DescriptorLength int    `json:"descriptor_length"`
					TextChar         string `json:"text_char"`
				} `json:"module_info"`
			} `json:"modules"`
			PrivateDataLength int `json:"private_data_length"`
		} `json:"dii"`
		DDB *struct {
			DownloadID    int    `json:"download_id"`
			ModuleID      int    `json:"module_id"`
			ModuleVersion int    `json:"module_version"`
			BlockNumber   int    `json:"block_number"`
			BlockData     string `json:"block_data"`
		} `json:"ddb"`
	} `json:"d"`
}

// TestDSMCCDifferentialOracle は ARIBTS_DSMCC_ORACLE=<jsonl> を指定すると、
// Node 版 aribts が出力した全 DSM-CC セクションについて Go の DecodeDSMCC の
// moduleInfo (tag / length / text_char バイト) と DDB フィールドが完全一致することを検証する。
//
// これは Node 版の「(バグ互換の) moduleInfo パース」を Go が再現していることの実データ検証で、
// LOGO-05 判定 (descriptor_tag === 0x02 && text_char === "LOGO-05") の挙動差が無いことの根拠になる。
func TestDSMCCDifferentialOracle(t *testing.T) {
	oraclePath := os.Getenv("ARIBTS_DSMCC_ORACLE")
	if oraclePath == "" {
		t.Skip("ARIBTS_DSMCC_ORACLE を設定すると DSM-CC 差分比較を実行する")
	}
	raw, err := os.ReadFile(oraclePath)
	if err != nil {
		t.Fatalf("オラクル読み込み失敗: %v", err)
	}

	var diiCount, ddbCount, matched int
	var mismatches []string

	for lineNo, ln := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var ol dsmccOracleLine
		if err := json.Unmarshal([]byte(ln), &ol); err != nil {
			t.Fatalf("オラクル行のパース失敗 (#%d): %v", lineNo, err)
		}
		if ol.Table != "dsmcc" {
			continue
		}
		sec, err := hex.DecodeString(ol.SectionHex)
		if err != nil {
			t.Fatalf("section_hex のデコード失敗 (#%d): %v", lineNo, err)
		}
		d, err := DecodeDSMCC(sec)
		if err != nil {
			mismatches = append(mismatches, fmt.Sprintf("#%d DecodeDSMCC 失敗: %v", lineNo, err))
			continue
		}
		switch ol.D.TableID {
		case 0x3B:
			diiCount++
			mismatches = append(mismatches, compareDII(lineNo, ol, d)...)
		case 0x3C:
			ddbCount++
			if diff := compareDDB(lineNo, ol, d); diff != "" {
				mismatches = append(mismatches, diff)
			} else {
				matched++
			}
		default:
			// 0x3D/0x3E などは今回の比較対象外
		}
	}

	t.Logf("DSM-CC 差分: DII=%d DDB=%d 一致=%d 不一致=%d (%s)", diiCount, ddbCount, matched, len(mismatches), oraclePath)
	if len(mismatches) > 0 {
		for i, m := range mismatches {
			if i >= 30 {
				t.Logf("  ... (残り %d 件省略)", len(mismatches)-i)
				break
			}
			t.Logf("  不一致: %s", m)
		}
		t.Fatalf("DSM-CC 差分: %d 件", len(mismatches))
	}
	if diiCount == 0 && ddbCount == 0 {
		t.Fatalf("DSM-CC セクションが 1 件も見つからない: %s", oraclePath)
	}
}

// compareDII は DII の modules / moduleInfo (tag,length,text_char) を Node と比較する。
func compareDII(lineNo int, ol dsmccOracleLine, d *DSMCC) []string {
	var out []string
	msg, ok := d.Message.(*DSMCCDownloadInfoIndication)
	if !ok {
		return []string{fmt.Sprintf("#%d DII Message 型 = %T", lineNo, d.Message)}
	}
	od := ol.D.DII
	if od == nil {
		return []string{fmt.Sprintf("#%d Node 側に dii が無い", lineNo)}
	}
	if msg.DownloadID != od.DownloadID || msg.BlockSize != od.BlockSize || msg.NumberOfModules != od.NumberOfModules {
		out = append(out, fmt.Sprintf("#%d DII ヘッダ不一致: go=(dl=%d bs=%d n=%d) node=(dl=%d bs=%d n=%d)",
			lineNo, msg.DownloadID, msg.BlockSize, msg.NumberOfModules, od.DownloadID, od.BlockSize, od.NumberOfModules))
	}
	if len(msg.Modules) != len(od.Modules) {
		out = append(out, fmt.Sprintf("#%d DII モジュール数不一致: go=%d node=%d", lineNo, len(msg.Modules), len(od.Modules)))
		return out
	}
	for i := range msg.Modules {
		gm, nm := msg.Modules[i], od.Modules[i]
		if gm.ModuleID != nm.ModuleID || gm.ModuleSize != nm.ModuleSize || gm.ModuleVersion != nm.ModuleVersion || gm.ModuleInfoLength != nm.ModuleInfoLength {
			out = append(out, fmt.Sprintf("#%d DII module[%d] 不一致: go=(id=%d size=%d ver=%d len=%d) node=(id=%d size=%d ver=%d len=%d)",
				lineNo, i, gm.ModuleID, gm.ModuleSize, gm.ModuleVersion, gm.ModuleInfoLength,
				nm.ModuleID, nm.ModuleSize, nm.ModuleVersion, nm.ModuleInfoLength))
		}
		if len(gm.ModuleInfo) != len(nm.ModuleInfo) {
			out = append(out, fmt.Sprintf("#%d DII module[%d] moduleInfo 数不一致: go=%d node=%d", lineNo, i, len(gm.ModuleInfo), len(nm.ModuleInfo)))
			continue
		}
		for j := range gm.ModuleInfo {
			gd, nd := gm.ModuleInfo[j], nm.ModuleInfo[j]
			if gd.Tag != nd.DescriptorTag || gd.Length != nd.DescriptorLength || hex.EncodeToString(gd.TextChar) != nd.TextChar {
				out = append(out, fmt.Sprintf("#%d DII module[%d].moduleInfo[%d] 不一致: go=(tag=%d len=%d text=%s) node=(tag=%d len=%d text=%s)",
					lineNo, i, j, gd.Tag, gd.Length, hex.EncodeToString(gd.TextChar), nd.DescriptorTag, nd.DescriptorLength, nd.TextChar))
			}
		}
	}
	return out
}

// compareDDB は DDB のフィールドを Node と比較する。
func compareDDB(lineNo int, ol dsmccOracleLine, d *DSMCC) string {
	msg, ok := d.Message.(*DSMCCDownloadDataBlock)
	if !ok {
		return fmt.Sprintf("#%d DDB Message 型 = %T", lineNo, d.Message)
	}
	od := ol.D.DDB
	if od == nil {
		return fmt.Sprintf("#%d Node 側に ddb が無い", lineNo)
	}
	if msg.DownloadID != od.DownloadID || msg.ModuleID != od.ModuleID || msg.ModuleVersion != od.ModuleVersion || msg.BlockNumber != od.BlockNumber || hex.EncodeToString(msg.BlockDataByte) != od.BlockData {
		return fmt.Sprintf("#%d DDB 不一致: go=(dl=%d mid=%d ver=%d blk=%d data=%s) node=(dl=%d mid=%d ver=%d blk=%d data=%s)",
			lineNo, msg.DownloadID, msg.ModuleID, msg.ModuleVersion, msg.BlockNumber, hex.EncodeToString(msg.BlockDataByte),
			od.DownloadID, od.ModuleID, od.ModuleVersion, od.BlockNumber, od.BlockData)
	}
	return ""
}
