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

package tuner

import (
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/aribts"
	"github.com/aki0429/Mirakurun/go/internal/core"
)

// STREAM_CONTENT はコンポーネント記述子の stream_content を映像種別名へ対応付ける (Node 版 EPG.ts)。
var streamContent = map[int]string{
	1: "mpeg2",
	5: "h.264",
	9: "h.265",
}

// componentTypeTable はコンポーネント記述子の component_type を解像度名へ対応付ける (Node 版 EPG.ts)。
var componentTypeTable = map[int]string{
	0x01: "480i", 0x02: "480i", 0x03: "480i", 0x04: "480i",
	0x83: "4320p", 0x91: "2160p", 0x92: "2160p", 0x93: "2160p", 0x94: "2160p",
	0xA1: "480p", 0xA2: "480p", 0xA3: "480p", 0xA4: "480p",
	0xB1: "1080i", 0xB2: "1080i", 0xB3: "1080i", 0xB4: "1080i",
	0xC1: "720p", 0xC2: "720p", 0xC3: "720p", 0xC4: "720p",
	0xD1: "240p", 0xD2: "240p", 0xD3: "240p", 0xD4: "240p",
	0xE1: "1080p", 0xE2: "1080p", 0xE3: "1080p", 0xE4: "1080p",
	0xF1: "180p", 0xF2: "180p", 0xF3: "180p", 0xF4: "180p",
}

// samplingRateTable は音声コンポーネント記述子の sampling_rate を Hz へ対応付ける (不明は -1)。
var samplingRateTable = map[int]int{
	0: -1, 1: 16000, 2: 22050, 3: 24000, 4: -1, 5: 32000, 6: 44100, 7: 48000,
}

// unknownStartTime / unknownDuration は値が不明な start_time / duration の表現。
var unknownStartTime = [5]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
var unknownDuration = [3]byte{0xFF, 0xFF, 0xFF}

// iso639LangCode は getLangCode が返す 3 文字コードを列挙順 (挿入順) で保持する。
var iso639LangCode = []string{"jpn", "eng", "deu", "fra", "ita", "rus", "zho", "kor", "spa", "etc"}

// versionRecord は Node 版 VersionRecord (table_id → version_number) に相当する。
type versionRecord map[int]int

// extendedState は拡張形式イベント記述子の組み立て状態。
type extendedState struct {
	version versionRecord
	// descs / present は descriptor_number ごとの項目配列と、そのスロットが設定済みかを表す。
	descs   [][]aribts.ExtendedEventItem
	present []bool
	done    bool
}

// eventState は 1 イベント分の集約状態 (Node 版 EventState)。
type eventState struct {
	version   versionRecord
	programID int64

	short     struct{ version versionRecord }
	extended  extendedState
	component struct{ version versionRecord }
	content   struct{ version versionRecord }
	audio     struct {
		version map[int]versionRecord
		audios  map[int]apid.ProgramAudio
	}
	series struct{ version versionRecord }
	group  struct {
		version map[int]versionRecord
		groups  [][]apid.ProgramRelatedItem
	}

	present   bool
	following bool
}

// EPG は EIT から番組情報を組み立てて core.Program / core.Service を更新する (Node 版 EPG.ts の移植)。
type EPG struct {
	mu  sync.Mutex
	app *core.App
	// epg は networkId → serviceId → eventId → 状態。
	epg map[int]map[int]map[int]*eventState
}

// newEPG は空の EPG を作成する。
func newEPG(app *core.App) *EPG {
	return &EPG{app: app, epg: map[int]map[int]map[int]*eventState{}}
}

// Write は EIT セクション 1 つ分を処理する。
func (e *EPG) Write(eit *aribts.EIT) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.epg == nil {
		return
	}

	isPF := eit.TableID == 0x4E || eit.TableID == 0x4F
	if isPF && eit.SectionNumber > 1 {
		return
	}
	isP := isPF && eit.SectionNumber == 0
	isF := isPF && eit.SectionNumber == 1

	networkID := eit.OriginalNetworkID
	if e.epg[networkID] == nil {
		e.epg[networkID] = map[int]map[int]*eventState{}
	}
	if e.epg[networkID][eit.ServiceID] == nil {
		e.epg[networkID][eit.ServiceID] = map[int]*eventState{}
	}
	service := e.epg[networkID][eit.ServiceID]

	for _, ev := range eit.Events {
		state, exists := service[ev.EventID]
		if !exists {
			id := core.GetProgramItemID(networkID, eit.ServiceID, ev.EventID)
			if !e.app.Program.Exists(id) {
				if ev.StartTime == unknownStartTime {
					continue
				}
				item := &apid.Program{
					ID:        id,
					EventID:   ev.EventID,
					ServiceID: eit.ServiceID,
					NetworkID: networkID,
					StartAt:   aribts.MJDToUnixMs(ev.StartTime[:]),
					Duration:  eventDuration(ev),
					IsFree:    ev.FreeCAMode == 0,
				}
				if isPF {
					item.PF = apid.Ptr(true)
				}
				if isP {
					item.IsPresent = apid.Ptr(true)
				}
				if isF {
					item.IsFollowing = apid.Ptr(true)
				}
				e.app.Program.Add(item, false)
			}

			state = &eventState{
				version:   versionRecord{},
				programID: id,
			}
			state.short.version = versionRecord{}
			state.extended.version = versionRecord{}
			state.component.version = versionRecord{}
			state.content.version = versionRecord{}
			state.audio.version = map[int]versionRecord{}
			state.audio.audios = map[int]apid.ProgramAudio{}
			state.series.version = versionRecord{}
			state.group.version = map[int]versionRecord{}
			state.group.groups = [][]apid.ProgramRelatedItem{}
			state.present = isP
			state.following = isF

			state.version[eit.TableID] = eit.VersionNumber
			service[ev.EventID] = state
		} else {
			if (!state.present && isP) || (!state.following && isF) || isOutOfDate(eit, state.version) {
				state.version[eit.TableID] = eit.VersionNumber

				if ev.StartTime != unknownStartTime {
					patch := core.ProgramPatch{
						StartAt:  apid.Ptr(aribts.MJDToUnixMs(ev.StartTime[:])),
						Duration: apid.Ptr(eventDuration(ev)),
						IsFree:   apid.Ptr(ev.FreeCAMode == 0),
					}
					if isPF {
						patch.PF = apid.Ptr(true)
					}
					if isP {
						patch.IsPresent = apid.Ptr(true)
					}
					if isF {
						patch.IsFollowing = apid.Ptr(true)
					}
					e.app.Program.Set(state.programID, patch)
				}

				state.present = isP
				state.following = isF
			}
		}

		for _, d := range ev.Descriptors {
			if d.Null || d.Value == nil {
				continue
			}
			switch d.Tag {
			case 0x4D: // short_event
				if !isOutOfDate(eit, state.short.version) {
					break
				}
				state.short.version[eit.TableID] = eit.VersionNumber
				v := d.Value.(*aribts.ShortEvent)
				e.app.Program.Set(state.programID, core.ProgramPatch{
					Name:        apid.Ptr(aribts.DecodeString(v.EventNameChar)),
					Description: apid.Ptr(aribts.DecodeString(v.TextChar)),
				})

			case 0x4E: // extended_event
				e.writeExtended(state, eit, d.Value.(*aribts.ExtendedEvent))

			case 0x50: // component
				if !isOutOfDate(eit, state.component.version) {
					break
				}
				state.component.version[eit.TableID] = eit.VersionNumber
				v := d.Value.(*aribts.Component)
				e.app.Program.Set(state.programID, core.ProgramPatch{
					Video: &apid.ProgramVideo{
						Type:          ptrStringMap(streamContent, v.StreamContent),
						Resolution:    ptrStringMap(componentTypeTable, v.ComponentType),
						StreamContent: v.StreamContent,
						ComponentType: v.ComponentType,
					},
				})

			case 0x54: // content
				if !isOutOfDate(eit, state.content.version) {
					break
				}
				state.content.version[eit.TableID] = eit.VersionNumber
				v := d.Value.(*aribts.Content)
				genres := make([]apid.ProgramGenre, 0, len(v.Contents))
				for _, c := range v.Contents {
					genres = append(genres, apid.ProgramGenre{Lv1: c.L1, Lv2: c.L2, Un1: c.U1, Un2: c.U2})
				}
				e.app.Program.Set(state.programID, core.ProgramPatch{Genres: genres})

			case 0xC4: // audio_component
				v := d.Value.(*aribts.AudioComponent)
				if !isOutOfDateLv2(eit, state.audio.version, v.ComponentTag) {
					break
				}
				state.audio.version[eit.TableID][v.ComponentTag] = eit.VersionNumber

				langs := []string{getLangCode(v.ISO639LanguageCode)}
				if v.Lang2 != nil {
					langs = append(langs, getLangCode(v.ISO639LanguageCode2))
				}
				state.audio.audios[v.ComponentTag] = apid.ProgramAudio{
					ComponentType: v.ComponentType,
					ComponentTag:  v.ComponentTag,
					IsMain:        v.MainComponentFlag == 1,
					SamplingRate:  samplingRateTable[v.SamplingRate],
					Langs:         langs,
				}
				e.app.Program.Set(state.programID, core.ProgramPatch{Audios: sortedAudios(state.audio.audios)})

			case 0xD5: // series
				if !isOutOfDate(eit, state.series.version) {
					break
				}
				state.series.version[eit.TableID] = eit.VersionNumber
				v := d.Value.(*aribts.Series)
				expiresAt := int64(-1)
				if v.ExpireDateValidFlag == 1 {
					expiresAt = aribts.MJDToUnixMs(hexBufferLikeNode(int(v.ExpireDate)))
				}
				e.app.Program.Set(state.programID, core.ProgramPatch{
					Series: &apid.ProgramSeries{
						ID:          v.SeriesID,
						Repeat:      v.RepeatLabel,
						Pattern:     v.ProgramPattern,
						ExpiresAt:   expiresAt,
						Episode:     v.EpisodeNumber,
						LastEpisode: v.LastEpisodeNumber,
						Name:        aribts.DecodeString(v.SeriesNameChar),
					},
				})

			case 0xD6: // event_group
				v := d.Value.(*aribts.EventGroup)
				if !isOutOfDateLv2(eit, state.group.version, v.GroupType) {
					break
				}
				state.group.version[eit.TableID][v.GroupType] = eit.VersionNumber
				items := []apid.ProgramRelatedItem{}
				if v.GroupType < 4 {
					for _, ge := range v.Events {
						items = append(items, relatedProgramItem(v.GroupType, 0, ge.ServiceID, ge.EventID))
					}
				} else {
					for _, ge := range v.OtherNetworkEvents {
						items = append(items, relatedProgramItem(v.GroupType, ge.OriginalNetworkID, ge.ServiceID, ge.EventID))
					}
				}
				for len(state.group.groups) <= v.GroupType {
					state.group.groups = append(state.group.groups, nil)
				}
				state.group.groups[v.GroupType] = items

				flat := []apid.ProgramRelatedItem{}
				for _, g := range state.group.groups {
					flat = append(flat, g...)
				}
				e.app.Program.Set(state.programID, core.ProgramPatch{RelatedItems: flat})
			}
		}
	}
}

// writeExtended は拡張形式イベント記述子を descriptor_number ごとに集めて extended を確定する。
func (e *EPG) writeExtended(state *eventState, eit *aribts.EIT, d *aribts.ExtendedEvent) {
	if isOutOfDate(eit, state.extended.version) {
		state.extended.version[eit.TableID] = eit.VersionNumber
		state.extended.descs = make([][]aribts.ExtendedEventItem, d.LastDescriptorNumber+1)
		state.extended.present = make([]bool, d.LastDescriptorNumber+1)
		state.extended.done = false
	} else if state.extended.done {
		return
	}

	num := d.DescriptorNumber
	if num < 0 || num >= len(state.extended.descs) || state.extended.present[num] {
		return
	}
	state.extended.descs[num] = d.Items
	state.extended.present[num] = true

	for _, ok := range state.extended.present {
		if !ok {
			return
		}
	}

	// 全 descriptor_number が揃ったので extended を組み立てる。
	// Node と同じく、キーは挿入順を保持し、項目は「連結したバイト列を 1 回デコード」してから
	// "\n\n" で連結する (連続項目はバッファを連結してからデコードする)。
	buffers := map[string][][]byte{}
	var order []string
	current := ""
	for _, descs := range state.extended.descs {
		for _, desc := range descs {
			isContinuation := desc.DescriptionLength == 0
			key := current
			if !isContinuation {
				key = aribts.DecodeString(desc.DescriptionChar)
			}
			current = key
			if _, ok := buffers[key]; !ok {
				buffers[key] = [][]byte{}
				order = append(order, key)
			}
			if isContinuation && len(buffers[key]) > 0 {
				last := len(buffers[key]) - 1
				buffers[key][last] = append(buffers[key][last], desc.ItemChar...)
			} else {
				buffers[key] = append(buffers[key], desc.ItemChar)
			}
		}
	}

	extended := map[string]string{}
	for _, key := range order {
		parts := make([]string, 0, len(buffers[key]))
		for _, b := range buffers[key] {
			parts = append(parts, aribts.DecodeString(b))
		}
		extended[key] = strings.Join(parts, "\n\n")
	}
	e.app.Program.Set(state.programID, core.ProgramPatch{Extended: extended})

	state.extended.descs = nil
	state.extended.present = nil
	state.extended.done = true
}

// eventDuration は duration を ms で返す。不明値なら Node と同じく 1 を返す。
func eventDuration(ev aribts.EITEvent) int64 {
	if ev.Duration == unknownDuration {
		return 1
	}
	return aribts.BCD24ToMs(ev.Duration[:])
}

// isOutOfDate は Node 版 isOutOfDate の移植。
func isOutOfDate(eit *aribts.EIT, vr versionRecord) bool {
	if _, ok := vr[0x4E]; ok && eit.TableID != 0x4E {
		return false
	}
	if _, ok := vr[0x4F]; ok && eit.TableID != 0x4E && eit.TableID != 0x4F {
		return false
	}
	v, ok := vr[eit.TableID]
	return !ok || v != eit.VersionNumber
}

// isOutOfDateLv2 は Node 版 isOutOfDateLv2 の移植。
func isOutOfDateLv2(eit *aribts.EIT, vr map[int]versionRecord, lv2 int) bool {
	if _, ok := vr[0x4E]; ok && eit.TableID != 0x4E {
		return false
	}
	if _, ok := vr[0x4F]; ok && eit.TableID != 0x4E && eit.TableID != 0x4F {
		return false
	}
	inner, ok := vr[eit.TableID]
	if !ok {
		inner = versionRecord{}
		vr[eit.TableID] = inner
	}
	v, ok := inner[lv2]
	return !ok || v != eit.VersionNumber
}

// getLangCode は 3 文字コードのバイト列を ISO-639 のキーへ戻す (未知は "etc")。
func getLangCode(b []byte) string {
	s := string(b)
	for _, code := range iso639LangCode {
		if code == s {
			return code
		}
	}
	return "etc"
}

// relatedProgramItem は Node 版 getRelatedProgramItem の移植。
// networkID が 0 のときは同一ネットワークのイベント (Node では undefined → JSON 省略)。
func relatedProgramItem(groupType, networkID, serviceID, eventID int) apid.ProgramRelatedItem {
	typ := "movement"
	switch {
	case groupType == 1:
		typ = "shared"
	case groupType == 2 || groupType == 4:
		typ = "relay"
	}
	item := apid.ProgramRelatedItem{Type: typ, ServiceID: serviceID, EventID: eventID}
	if groupType >= 4 {
		item.NetworkID = apid.Ptr(networkID)
	}
	return item
}

// ptrStringMap は table に値があればポインタを返し、無ければ nil (=JSON null) を返す。
func ptrStringMap(table map[int]string, key int) *string {
	if v, ok := table[key]; ok {
		return &v
	}
	return nil
}

// sortedAudios は component_tag の昇順で音声情報を返す (JS Object.values の整数キー昇順と同じ)。
func sortedAudios(audios map[int]apid.ProgramAudio) []apid.ProgramAudio {
	keys := make([]int, 0, len(audios))
	for k := range audios {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	out := make([]apid.ProgramAudio, 0, len(keys))
	for _, k := range keys {
		out = append(out, audios[k])
	}
	return out
}

// hexBufferLikeNode は Node の Buffer.from(number.toString(16), "hex") を再現する。
// 16 進表記が奇数長のときは末尾 1 ニブルが無視され、その結果は空または偶数長のバイト列になる。
func hexBufferLikeNode(v int) []byte {
	s := strconv.FormatUint(uint64(v), 16)
	if len(s)%2 == 1 {
		s = s[:len(s)-1]
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}
