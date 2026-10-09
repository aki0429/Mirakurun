/*
   Copyright 2016 kanreisa
   Copyright 2026 aki0429 (Go port)

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
)

// genreLV1 は Node 版 xmltv.ts の GENRE_LV1。
var genreLV1 = map[int]string{
	0x0: "ニュース／報道", 0x1: "スポーツ", 0x2: "情報／ワイドショー", 0x3: "ドラマ",
	0x4: "音楽", 0x5: "バラエティ", 0x6: "映画", 0x7: "アニメ／特撮",
	0x8: "ドキュメンタリー／教養", 0x9: "劇場／公演", 0xA: "趣味／教育", 0xB: "福祉",
	0xC: "予備", 0xD: "予備", 0xE: "拡張", 0xF: "その他",
}

// genreLV2 は Node 版 xmltv.ts の GENRE_LV2 (キーは lv1*0x10+lv2)。
var genreLV2 = map[int]string{
	0x00: "定時・総合", 0x01: "天気", 0x02: "特集・ドキュメント", 0x03: "政治・国会",
	0x04: "経済・市況", 0x05: "海外・国際", 0x06: "解説", 0x07: "討論・会談",
	0x08: "報道特番", 0x09: "ローカル・地域", 0x0A: "交通", 0x0F: "その他",
	0x10: "スポーツニュース", 0x11: "野球", 0x12: "サッカー", 0x13: "ゴルフ",
	0x14: "その他の球技", 0x15: "相撲・格闘技", 0x16: "オリンピック・国際大会",
	0x17: "マラソン・陸上・水泳", 0x18: "モータースポーツ", 0x19: "マリン・ウィンタースポーツ",
	0x1A: "競馬・公営競技", 0x1F: "その他",
	0x20: "芸能・ワイドショー", 0x21: "ファッション", 0x22: "暮らし・住まい", 0x23: "健康・医療",
	0x24: "ショッピング・通販", 0x25: "グルメ・料理", 0x26: "イベント", 0x27: "番組紹介・お知らせ", 0x2F: "その他",
	0x30: "国内ドラマ", 0x31: "海外ドラマ", 0x32: "時代劇", 0x3F: "その他",
	0x40: "国内ロック・ポップス", 0x41: "海外ロック・ポップス", 0x42: "クラシック・オペラ", 0x43: "ジャズ・フュージョン",
	0x44: "歌謡曲・演歌", 0x45: "ライブ・コンサート", 0x46: "ランキング・リクエスト", 0x47: "カラオケ・のど自慢",
	0x48: "民謡・邦楽", 0x49: "童謡・キッズ", 0x4A: "民族音楽・ワールドミュージック", 0x4F: "その他",
	0x50: "クイズ", 0x51: "ゲーム", 0x52: "トークバラエティ", 0x53: "お笑い・コメディ",
	0x54: "音楽バラエティ", 0x55: "旅バラエティ", 0x56: "料理バラエティ", 0x5F: "その他",
	0x60: "洋画", 0x61: "邦画", 0x62: "アニメ", 0x6F: "その他",
	0x70: "国内アニメ", 0x71: "海外アニメ", 0x72: "特撮", 0x7F: "その他",
	0x80: "社会・時事", 0x81: "歴史・紀行", 0x82: "自然・動物・環境", 0x83: "宇宙・科学・医学",
	0x84: "カルチャー・伝統文化", 0x85: "文学・文芸", 0x86: "スポーツ", 0x87: "ドキュメンタリー全般",
	0x88: "インタビュー・討論", 0x8F: "その他",
	0x90: "現代劇・新劇", 0x91: "ミュージカル", 0x92: "ダンス・バレエ", 0x93: "落語・演芸",
	0x94: "歌舞伎・古典", 0x9F: "その他",
	0xA0: "旅・釣り・アウトドア", 0xA1: "園芸・ペット・手芸", 0xA2: "音楽・美術・工芸", 0xA3: "囲碁・将棋",
	0xA4: "麻雀・パチンコ", 0xA5: "車・オートバイ", 0xA6: "コンピュータ・ＴＶゲーム", 0xA7: "会話・語学",
	0xA8: "幼児・小学生", 0xA9: "中学生・高校生", 0xAA: "大学生・受験", 0xAB: "生涯教育・資格",
	0xAC: "教育問題", 0xAF: "その他",
	0xB0: "高齢者", 0xB1: "障害者", 0xB2: "社会福祉", 0xB3: "ボランティア",
	0xB4: "手話", 0xB5: "文字（字幕）", 0xB6: "音声解説", 0xBF: "その他",
	0xE0: "BS/地上デジタル放送用番組付属情報", 0xE1: "広帯域CSデジタル放送用拡張",
	0xE2: "衛星デジタル音声放送用拡張", 0xE3: "サーバー型番組付属情報",
	0xE4: "IP放送用番組付属情報", 0xF0: "その他", 0xFF: "その他",
}

// genreUnex は Node 版 xmltv.ts の GENRE_UNEX。
var genreUnex = map[int]string{
	0x000: "中止の可能性あり", 0x001: "延長の可能性あり", 0x002: "中断の可能性あり",
	0x003: "同一シリーズの別話数放送の可能性あり", 0x004: "編成未定枠", 0x005: "繰り上げの可能性あり",
	0x011: "当該イベントに関連する臨時サービスあり",
	0x100: "スポーツ - テニス", 0x101: "スポーツ - バスケットボール", 0x102: "スポーツ - ラグビー",
	0x103: "スポーツ - アメリカンフットボール", 0x104: "スポーツ - ボクシング", 0x105: "スポーツ - プロレス",
	0x10F: "スポーツ - その他",
	0x110: "洋画 - アクション", 0x111: "洋画 - ＳＦ／ファンタジー", 0x112: "洋画 - コメディー",
	0x113: "洋画 - サスペンス／ミステリー", 0x114: "洋画 - 恋愛／ロマンス", 0x115: "洋画 - ホラー／スリラー",
	0x116: "洋画 - ウエスタン", 0x117: "洋画 - ドラマ／社会派ドラマ", 0x118: "洋画 - アニメーション",
	0x119: "洋画 - ドキュメンタリー", 0x11A: "洋画 - アドベンチャー／冒険", 0x11B: "洋画 - ミュージカル／音楽映画",
	0x11C: "洋画 - ホームドラマ", 0x11F: "洋画 - その他",
	0x120: "邦画 - アクション", 0x121: "邦画 - ＳＦ／ファンタジー", 0x122: "邦画 - お笑い／コメディー",
	0x123: "邦画 - サスペンス／ミステリー", 0x124: "邦画 - 恋愛／ロマンス", 0x125: "邦画 - ホラー／スリラー",
	0x126: "邦画 - 青春／学園／アイドル", 0x127: "邦画 - 任侠／時代劇", 0x128: "邦画 - アニメーション",
	0x129: "邦画 - ドキュメンタリー", 0x12A: "邦画 - アドベンチャー／冒険", 0x12B: "邦画 - ミュージカル／音楽映画",
	0x12C: "邦画 - ホームドラマ", 0x12F: "邦画 - その他",
}

func escapeXMLSpecialChars(str string) string {
	str = strings.ReplaceAll(str, "&", "&amp;")
	str = strings.ReplaceAll(str, "<", "&lt;")
	str = strings.ReplaceAll(str, ">", "&gt;")
	str = strings.ReplaceAll(str, "\"", "&quot;")
	str = strings.ReplaceAll(str, "'", "&apos;")
	return str
}

// getDateTime は Node 版 getDateTime (ISO8601 UTC, ミリ秒なし, " +0000") を再現する。
func getDateTime(t int64) string {
	return time.UnixMilli(t).UTC().Format("20060102150405") + " +0000"
}

// getGenreStrings は Node 版 getGenreStrings を再現する (挿入順を保って重複排除)。
func getGenreStrings(genres []apid.ProgramGenre) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, g := range genres {
		if g.Lv1 == 14 {
			if text, ok := genreUnex[(g.Lv2*0x100)+(g.Un1*0x10)+g.Un2]; ok {
				add(text)
			}
		} else {
			lv1, ok1 := genreLV1[g.Lv1]
			lv2, ok2 := genreLV2[g.Lv1*0x10+g.Lv2]
			if !ok1 {
				lv1 = "undefined"
			}
			if !ok2 {
				lv2 = "undefined"
			}
			add(lv1 + " - " + lv2)
		}
	}
	return out
}

// handleIPTVXMLTV は GET /api/iptv/xmltv (src/Mirakurun/api/iptv/xmltv.ts)。
func (s *Server) handleIPTVXMLTV(w http.ResponseWriter, r *http.Request) {
	root := apiRoot(r)

	var x strings.Builder
	x.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	x.WriteString("<!DOCTYPE tv SYSTEM \"xmltv.dtd\">\n")
	x.WriteString("<tv source-info-name=\"Mirakurun\">\n")

	countMap := map[int]int{}
	for _, svc := range s.sortedServiceItems() {
		t := svc.Type()
		if t == nil || (*t != 1 && *t != 173) {
			continue
		}
		mainNum := mainChannelNumber(svc)
		countMap[mainNum]++
		subNum := countMap[mainNum]

		x.WriteString("<channel id=\"" + itoa64(svc.ID()) + "\">\n")
		x.WriteString("<display-name>" + escapeXMLSpecialChars(svc.Name()) + "</display-name>\n")
		x.WriteString("<display-name>" + itoa(mainNum) + "." + itoa(subNum) + "</display-name>\n")
		if s.hasLogoData(svc) {
			x.WriteString("<icon src=\"" + root + "/services/" + itoa64(svc.ID()) + "/logo\" />")
		}
		x.WriteString("</channel>\n")
	}

	programs, err := s.app.Program.FindByQuery(map[string]string{})
	if err != nil {
		s.responseError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, prog := range programs {
		svc := s.app.Service.GetByNetworkID(prog.NetworkID, prog.ServiceID)
		if svc == nil {
			continue
		}
		x.WriteString("<programme start=\"" + getDateTime(prog.StartAt) + "\" stop=\"" + getDateTime(prog.StartAt+prog.Duration) + "\" channel=\"" + itoa64(svc.ID()) + "\">\n")
		name := ""
		if prog.Name != nil {
			name = *prog.Name
		}
		desc := ""
		if prog.Description != nil {
			desc = *prog.Description
		}
		x.WriteString("<title>" + escapeXMLSpecialChars(name) + "</title>\n")
		x.WriteString("<desc>" + escapeXMLSpecialChars(desc) + "</desc>\n")
		if len(prog.Genres) > 0 {
			for _, gs := range getGenreStrings(prog.Genres) {
				x.WriteString("<category>" + gs + "</category>\n")
			}
		}
		x.WriteString("</programme>\n")
	}

	x.WriteString("</tv>")

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(x.String()))
}
