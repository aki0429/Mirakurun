package apid

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// OrderedStringMap は挿入順を保持する string→string マップ。
// Program.extended は JavaScript オブジェクトのキー順 (挿入順) で出力されるため、
// Go の map では順序が崩れてしまう。これを避けるための型。
type OrderedStringMap struct {
	keys   []string
	values map[string]string
}

// Set はキーを設定する。既存キーの上書きは順序を変えない。
func (m *OrderedStringMap) Set(k, v string) {
	if m.values == nil {
		m.values = map[string]string{}
	}
	if _, ok := m.values[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.values[k] = v
}

// Get は値を返す。
func (m *OrderedStringMap) Get(k string) (string, bool) {
	v, ok := m.values[k]
	return v, ok
}

// Keys は挿入順のキー一覧を返す。
func (m *OrderedStringMap) Keys() []string { return append([]string(nil), m.keys...) }

// Len は要素数を返す。
func (m *OrderedStringMap) Len() int { return len(m.keys) }

// MarshalJSON は挿入順でオブジェクトを出力する。
func (m *OrderedStringMap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		vb, err := json.Marshal(m.values[k])
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// UnmarshalJSON はオブジェクトのキー順を保って読み込む。
func (m *OrderedStringMap) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("OrderedStringMap: expected object")
	}
	m.keys = nil
	m.values = map[string]string{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		k, ok := kt.(string)
		if !ok {
			return fmt.Errorf("OrderedStringMap: invalid key")
		}
		var v string
		if err := dec.Decode(&v); err != nil {
			return err
		}
		m.Set(k, v)
	}
	_, err = dec.Token()
	return err
}
