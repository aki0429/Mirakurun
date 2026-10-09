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

package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// orderedMap は挿入順 (YAML のキー順) を保持する JS オブジェクト相当。
// Node 版は channels.yml を yaml.load した結果を JSON.stringify するため、
// キー順とスカラーの型 (string / number / boolean / null) がそのまま
// ハッシュへ影響する。Go の map では順序が失われるので専用の型を使う。
type orderedMap struct {
	keys []string
	vals map[string]any
}

func (m *orderedMap) set(k string, v any) {
	if m.vals == nil {
		m.vals = map[string]any{}
	}
	if _, ok := m.vals[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.vals[k] = v
}

// parseYAML は YAML を js-yaml 互換のジェネリック値へ変換する。
// 返り値の型は nil / bool / string / int64 / float64 / []any / *orderedMap のいずれか。
func parseYAML(data []byte) (any, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if doc.Kind == 0 {
		// 空ドキュメント → js-yaml は undefined を返す。JSON.stringify(undefined) は
		// 文字列 "undefined" になるが、実際の channels.yml は空にならないため null 扱いとする。
		return nil, nil
	}
	return parseNode(&doc)
}

func parseNode(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return parseNode(n.Content[0])
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := parseNode(c)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.MappingNode:
		m := &orderedMap{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			v, err := parseNode(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			m.set(k, v)
		}
		return m, nil
	case yaml.AliasNode:
		return parseNode(n.Alias)
	case yaml.ScalarNode:
		return parseScalar(n)
	default:
		return nil, fmt.Errorf("unsupported yaml node kind: %d", n.Kind)
	}
}

// parseScalar はスカラーノードを js-yaml と同じ型解決で Go 値にする。
// 明示タグ (引用符付き文字列など) はそのまま尊重する。
func parseScalar(n *yaml.Node) (any, error) {
	switch n.Tag {
	case "!!null":
		return nil, nil
	case "!!bool":
		return strconv.ParseBool(n.Value)
	case "!!int":
		v, err := strconv.ParseInt(n.Value, 10, 64)
		if err != nil {
			// 8 進 / 16 進などは JS 側の型解決とずれるため、文字列として保持せずエラーにする。
			return nil, fmt.Errorf("parse int %q: %w", n.Value, err)
		}
		return v, nil
	case "!!float":
		// YAML の .inf / .nan は JSON.stringify で null になる。
		f, err := parseFloatJS(n.Value)
		if err != nil {
			return nil, err
		}
		return f, nil
	case "!!str":
		return n.Value, nil
	default:
		// タイムスタンプやバイナリなどは js-yaml が Date/Buffer に解決するが、
		// 本用途 (channels.yml) では現れないため文字列として扱う。
		return n.Value, nil
	}
}

func parseFloatJS(s string) (float64, error) {
	switch s {
	case ".inf", ".Inf", ".INF", "+.inf", "+.Inf", "+.INF":
		return math.Inf(1), nil
	case "-.inf", "-.Inf", "-.INF":
		return math.Inf(-1), nil
	case ".nan", ".NaN", ".NAN":
		return math.NaN(), nil
	}
	return strconv.ParseFloat(s, 64)
}

// stringifyJS は JSON.stringify と同一の文字列を返す。
// Go の encoding/json は既定で <, >, & を \u00xx にエスケープし、
// 大きな整数を float 化する可能性があるため使わない。
func stringifyJS(v any) (string, error) {
	var buf bytes.Buffer
	if err := writeJS(&buf, v); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func writeJS(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if x {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		appendJSString(buf, x)
	case int64:
		buf.WriteString(strconv.FormatInt(x, 10))
	case int:
		buf.WriteString(strconv.Itoa(x))
	case float64:
		buf.WriteString(formatJSNumber(x))
	case []any:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeJS(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case *orderedMap:
		buf.WriteByte('{')
		for i, k := range x.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			appendJSString(buf, k)
			buf.WriteByte(':')
			if err := writeJS(buf, x.vals[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("stringifyJS: unsupported type %T", v)
	}
	return nil
}

// appendJSString は JSON.stringify の文字列エスケープを再現する。
// "," と "\" と制御文字のみをエスケープし、<, >, &, /, U+2028, U+2029 は素通しする。
func appendJSString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			buf.WriteString("\\\"")
		case '\\':
			buf.WriteString("\\\\")
		case '\b':
			buf.WriteString("\\b")
		case '\f':
			buf.WriteString("\\f")
		case '\n':
			buf.WriteString("\\n")
		case '\r':
			buf.WriteString("\\r")
		case '\t':
			buf.WriteString("\\t")
		default:
			if c < 0x20 {
				fmt.Fprintf(buf, "\\u%04x", c)
			} else {
				buf.WriteByte(c)
			}
		}
	}
	buf.WriteByte('"')
}

// formatJSNumber は JS の Number#toString 相当の最短表現を返す。
// 整数は整数表記、それ以外は Go の最短表現を JS の指数表記規則へ寄せる。
func formatJSNumber(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		// JSON.stringify(NaN) / JSON.stringify(Infinity) は "null"。
		return "null"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	// Go は指数部を "e-07" のように 2 桁ゼロ埋めするが JS は "e-7"。
	// また JS は |exp| < 6 では指数表記を使わない。ここでは指数部の整形のみ行う。
	return normalizeExponent(s)
}

func normalizeExponent(s string) string {
	i := bytes.IndexAny([]byte(s), "eE")
	if i < 0 {
		return s
	}
	mant := s[:i]
	exp := s[i+1:]
	sign := ""
	if len(exp) > 0 && (exp[0] == '+' || exp[0] == '-') {
		sign = string(exp[0])
		exp = exp[1:]
	}
	for len(exp) > 1 && exp[0] == '0' {
		exp = exp[1:]
	}
	return mant + "e" + sign + exp
}

// ChannelsIntegrity は Node 版
// `createHash("sha256").update(JSON.stringify(yaml.load(channelsYAML))).digest("base64")`
// と同一の値を返す。本番 services.json / programs.json が埋め込む __integrity__ と
// 一致させるための関数であり、Node が書いた DB を Go 側で読むために必須。
func ChannelsIntegrity(yamlData []byte) (string, error) {
	v, err := parseYAML(yamlData)
	if err != nil {
		return "", err
	}
	s, err := stringifyJS(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(s))
	return base64.StdEncoding.EncodeToString(sum[:]), nil
}

// ChannelsIntegrityFromFile はファイルを読んで ChannelsIntegrity を計算する。
func ChannelsIntegrityFromFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return ChannelsIntegrity(data)
}
