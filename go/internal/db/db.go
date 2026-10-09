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

// Package db は Node 版 db.ts の移植。
//
// services.json / programs.json は先頭要素に {"__integrity__": "..."} を持つ JSON 配列で、
// integrity が一致しなければ空を返す。壊れた JSON は空を返してエラーログを出す
// (Node 版と同じく例外は投げず、サーバーを継続させる)。
//
// 差分: Node 版は writeFile 直書きだが、Go 版は一時ファイル + rename で原子的に書く。
// 出力は Node 版と同じコンパクトな JSON 配列形式 (改行なし, HTML エスケープなし)。
package db

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/mlog"
)

// ServicesPath は Node 版 src/server.ts の setEnv("SERVICES_DB_PATH", ...) と同じ既定値。
const ServicesPath = "/usr/local/var/db/mirakurun/services.json"

// ProgramsPath は Node 版 src/server.ts の setEnv("PROGRAMS_DB_PATH", ...) と同じ既定値。
const ProgramsPath = "/usr/local/var/db/mirakurun/programs.json"

// LogoDataDirPath は Node 版 src/server.ts の setEnv("LOGO_DATA_DIR_PATH", ...) と同じ既定値。
const LogoDataDirPath = "/usr/local/var/db/mirakurun/logo-data"

// LoadServices は services.json を integrity 検証つきで読み込む。
func LoadServices(path, integrity string) ([]apid.Service, error) {
	return load[apid.Service](path, integrity)
}

// SaveServices は services.json を integrity ヘッダ付きで書き出す。
func SaveServices(path string, data []apid.Service, integrity string) error {
	return save(path, data, integrity)
}

// LoadPrograms は programs.json を integrity 検証つきで読み込む。
func LoadPrograms(path, integrity string) ([]apid.Program, error) {
	return load[apid.Program](path, integrity)
}

// SavePrograms は programs.json を integrity ヘッダ付きで書き出す。
func SavePrograms(path string, data []apid.Program, integrity string) error {
	return save(path, data, integrity)
}

// loadG は Node 版 load のジェネリック実装。
func load[T any](path, integrity string) ([]T, error) {
	mlog.Info("load db `%s` w/ integrity (%s)", path, integrity)

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		mlog.Info("db `%s` is not exists", path)
		return []T{}, nil
	}
	if err != nil {
		mlog.Error("db `%s` is broken (%s)", path, err)
		return []T{}, nil
	}

	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		mlog.Error("db `%s` is broken (%s)", path, err)
		return []T{}, nil
	}

	items := arr
	if len(arr) > 0 {
		var hdr struct {
			Integrity *string `json:"__integrity__"`
		}
		if json.Unmarshal(arr[0], &hdr) == nil && hdr.Integrity != nil {
			if integrity != *hdr.Integrity {
				mlog.Warn("db `%s` integrity check has failed", path)
				return []T{}, nil
			}
			items = arr[1:]
		}
	}

	out := make([]T, 0, len(items))
	for i, it := range items {
		var v T
		if err := json.Unmarshal(it, &v); err != nil {
			mlog.Error("db `%s` item #%d is broken (%s)", path, i, err)
			return []T{}, nil
		}
		out = append(out, v)
	}
	return out, nil
}

// save は Node 版 save の移植。ヘッダを先頭に付け、一時ファイル経由で原子的に書く。
func save[T any](path string, data []T, integrity string) error {
	mlog.Info("save db `%s` w/ integrity (%s)", path, integrity)

	body, err := marshalNoEscape(data)
	if err != nil {
		return fmt.Errorf("save db `%s`: %w", path, err)
	}

	header := `{"__integrity__":` + mustJSONString(integrity) + `}`
	var buf bytes.Buffer
	buf.Grow(len(body) + len(header) + 4)
	buf.WriteByte('[')
	buf.WriteString(header)
	if len(data) > 0 {
		buf.WriteByte(',')
		// body は必ず "[" で始まり "]" で終わるので、先頭の "[" を落として連結する。
		buf.Write(body[1:])
	} else {
		buf.WriteByte(']')
	}

	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("save db `%s`: %w", path, err)
		}
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("save db `%s`: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("save db `%s`: %w", path, err)
	}
	return nil
}

// marshalNoEscape は json.Marshal と同等だが HTML エスケープ (<, >, &) を行わない。
// Node 版 JSON.stringify に合わせるため。
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	return b, nil
}

func mustJSONString(s string) string {
	b, err := marshalNoEscape(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
