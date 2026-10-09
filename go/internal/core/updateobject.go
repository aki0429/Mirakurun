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

package core

import (
	"reflect"

	"github.com/aki0429/Mirakurun/go/internal/apid"
)

// applyPatch は Node 版 common.updateObject と同じ意味で patch を p へ適用し、
// 実際に変化があった場合のみ true を返す (変化が無ければ emit/save しない)。
func applyPatch(p *apid.Program, patch ProgramPatch) bool {
	changed := false

	// スカラー (数値・真偽・文字列)
	if setInt64Value(&p.StartAt, patch.StartAt) {
		changed = true
	}
	if setInt64Value(&p.Duration, patch.Duration) {
		changed = true
	}
	if setBoolValue(&p.IsFree, patch.IsFree) {
		changed = true
	}
	if setBool(&p.PF, patch.PF) {
		changed = true
	}
	if setBool(&p.IsPresent, patch.IsPresent) {
		changed = true
	}
	if setBool(&p.IsFollowing, patch.IsFollowing) {
		changed = true
	}
	if setString(&p.Name, patch.Name) {
		changed = true
	}
	if setString(&p.Description, patch.Description) {
		changed = true
	}

	// オブジェクト (video / series)
	if patch.Video != nil {
		if p.Video == nil {
			p.Video = patch.Video
			changed = true
		} else if mergeVideo(p.Video, patch.Video) {
			changed = true
		}
	}
	if patch.Series != nil {
		if p.Series == nil {
			p.Series = patch.Series
			changed = true
		} else if mergeSeries(p.Series, patch.Series) {
			changed = true
		}
	}

	// extended (順序付きマップ) はキー単位でマージする
	if patch.Extended != nil {
		if p.Extended == nil {
			p.Extended = &apid.OrderedStringMap{}
		}
		for k, v := range patch.Extended {
			if old, ok := p.Extended.Get(k); !ok || old != v {
				p.Extended.Set(k, v)
				changed = true
			}
		}
	}

	// 配列 (genres / audios / relatedItems) は updateArray と同じ挙動
	if patch.Genres != nil {
		if patchArray(&p.Genres, patch.Genres) {
			changed = true
		}
	}
	if patch.Audios != nil {
		if patchArray(&p.Audios, patch.Audios) {
			changed = true
		}
	}
	if patch.RelatedItems != nil {
		if patchArray(&p.RelatedItems, patch.RelatedItems) {
			changed = true
		}
	}

	return changed
}

func setInt64Value(dst *int64, src *int64) bool {
	if src == nil || *dst == *src {
		return false
	}
	*dst = *src
	return true
}

func setBoolValue(dst *bool, src *bool) bool {
	if src == nil || *dst == *src {
		return false
	}
	*dst = *src
	return true
}

func setBool(dst **bool, src *bool) bool {
	if src == nil {
		return false
	}
	if *dst != nil && **dst == *src {
		return false
	}
	v := *src
	*dst = &v
	return true
}

func setString(dst **string, src *string) bool {
	if src == nil {
		return false
	}
	if *dst != nil && **dst == *src {
		return false
	}
	v := *src
	*dst = &v
	return true
}

// mergeVideo は video オブジェクトのフィールド単位マージ。
func mergeVideo(dst, src *apid.ProgramVideo) bool {
	changed := false
	if setString(&dst.Type, src.Type) {
		changed = true
	}
	if setString(&dst.Resolution, src.Resolution) {
		changed = true
	}
	if dst.StreamContent != src.StreamContent {
		dst.StreamContent = src.StreamContent
		changed = true
	}
	if dst.ComponentType != src.ComponentType {
		dst.ComponentType = src.ComponentType
		changed = true
	}
	return changed
}

// mergeSeries は series オブジェクトのフィールド単位マージ。
func mergeSeries(dst, src *apid.ProgramSeries) bool {
	changed := false
	if dst.ID != src.ID {
		dst.ID = src.ID
		changed = true
	}
	if dst.Repeat != src.Repeat {
		dst.Repeat = src.Repeat
		changed = true
	}
	if dst.Pattern != src.Pattern {
		dst.Pattern = src.Pattern
		changed = true
	}
	if dst.ExpiresAt != src.ExpiresAt {
		dst.ExpiresAt = src.ExpiresAt
		changed = true
	}
	if dst.Episode != src.Episode {
		dst.Episode = src.Episode
		changed = true
	}
	if dst.LastEpisode != src.LastEpisode {
		dst.LastEpisode = src.LastEpisode
		changed = true
	}
	if dst.Name != src.Name {
		dst.Name = src.Name
		changed = true
	}
	return changed
}

// patchArray は updateArray 相当。長さが違えば全体置換、同じなら要素単位で更新する。
func patchArray[T any](dst *[]T, src []T) bool {
	if len(*dst) != len(src) {
		*dst = src
		return true
	}
	changed := false
	for i := range *dst {
		if !reflect.DeepEqual((*dst)[i], src[i]) {
			(*dst)[i] = src[i]
			changed = true
		}
	}
	return changed
}
