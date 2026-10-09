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

// Package tuner は Node 版 Mirakurun の Tuner / TunerDevice / TSFilter / TSDecoder / EPG を移植する。
//
// チューナーデバイスは tuners.yml の command を外部プロセスとして起動し、その stdout から
// 生の 188 バイト TS パケットを読み取って利用者 (ストリーム) へ中継する。セクションの組み立てや
// PSI/SI のデコードは internal/aribts に委譲する (ここでは再実装しない)。
package tuner

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// commandTemplatePattern は Node 版 common.replaceCommandTemplate の /<([a-z0-9\-_\.]+)>/gi に相当する。
var commandTemplatePattern = regexp.MustCompile(`(?i)<([a-z0-9\-_.]+)>`)

// ReplaceCommandTemplate はコマンド文字列中の <key> を vars の値で置換する。
// Node 版と同じく、値が未定義のキーは空文字列に、置換対象外の文字はそのまま残す。
func ReplaceCommandTemplate(template string, vars map[string]any) string {
	return commandTemplatePattern.ReplaceAllStringFunc(template, func(match string) string {
		key := match[1 : len(match)-1]
		v, ok := lookupVar(vars, key)
		if !ok {
			return ""
		}
		return stringifyNode(v)
	})
}

// lookupVar は vars から key を引く。Node は大文字小文字を区別するが、
// テンプレート側の表記ゆれを吸収するため厳密一致で見つからない場合のみ小文字一致を試す。
func lookupVar(vars map[string]any, key string) (any, bool) {
	if v, ok := vars[key]; ok {
		return v, true
	}
	for k, v := range vars {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return nil, false
}

// stringifyNode は JavaScript の String(value) と同じ文字列表現を返す。
// 数値は整数なら小数点なし、真偽値は "true"/"false"、文字列はそのまま。
func stringifyNode(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(t)
	case int8:
		return strconv.FormatInt(int64(t), 10)
	case int16:
		return strconv.FormatInt(int64(t), 10)
	case int32:
		return strconv.FormatInt(int64(t), 10)
	case int64:
		return strconv.FormatInt(t, 10)
	case uint:
		return strconv.FormatUint(uint64(t), 10)
	case uint64:
		return strconv.FormatUint(t, 10)
	case float32:
		return formatJSFloat(float64(t))
	case float64:
		return formatJSFloat(t)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

// formatJSFloat は整数値の float を "1e+06" ではなく "1000000" の形にする。
func formatJSFloat(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// ParseCommandForSpawn はコマンド文字列を Node 版 common.parseCommandForSpawn と同じ規則で
// command と args に分割する。空白が区切りで、引用符 (" または ') の内側の空白は区切りにならない。
// 引用符自体は出力に含めない。分割結果が空ならエラーを返す。
func ParseCommandForSpawn(cmdString string) (string, []string, error) {
	inQuote := false
	var quoteChar byte
	var current []byte
	parts := []string{}

	for i := 0; i < len(cmdString); i++ {
		ch := cmdString[i]
		if (ch == '"' || ch == '\'') && (!inQuote || ch == quoteChar) {
			inQuote = !inQuote
			if inQuote {
				quoteChar = ch
			} else {
				quoteChar = 0
			}
		} else if ch == ' ' && !inQuote {
			if len(current) > 0 {
				parts = append(parts, string(current))
				current = current[:0]
			}
		} else {
			current = append(current, ch)
		}
	}
	if len(current) > 0 {
		parts = append(parts, string(current))
	}
	if len(parts) == 0 {
		return "", nil, errors.New("invalid command string")
	}
	return parts[0], parts[1:], nil
}
