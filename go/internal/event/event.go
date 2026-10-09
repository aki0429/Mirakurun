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

// Package event は Node 版 Event.ts の移植。
//
// Emit 時点でデータを JSON 化する (Node 版の deepClone 相当) ため、
// 発行後に元オブジェクトを変更しても購読者・履歴には影響しない。
//
// 購読者 (OnEvent) にはバッファ付きチャネルで非ブロッキング送信する。
// バッファが満杯の購読者へのイベントは破棄して Dropped() を増やす。
// Node 版は同期 EventEmitter なので取りこぼしは起きないが、Go 版では
// 遅い /api/events/stream クライアントが Emit 側 (EPG 処理等) を止めないことを優先した。
package event

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/mlog"
)

// MaxLog は保持するイベント履歴数 (Node 版と同じ 100)。
const MaxLog = 100

// Options は Emitter の生成オプション。
type Options struct {
	// Now は現在時刻 (テスト用)。nil なら time.Now。
	Now func() time.Time
}

// Emitter はイベントの発行・履歴・購読を管理する。並行利用可能。
type Emitter struct {
	mu        sync.Mutex
	now       func() time.Time
	log       []apid.Event
	subs      map[int]chan apid.Event
	nextSubID int
	dropped   atomic.Int64
}

// New は Emitter を作る。
func New(opts Options) *Emitter {
	e := &Emitter{now: opts.Now, subs: map[int]chan apid.Event{}}
	if e.now == nil {
		e.now = time.Now
	}
	return e
}

// Emit はイベントを発行する。data は即座に JSON 化される。
// json.RawMessage はそのまま使う。JSON 化に失敗した場合は error ログを出して null を送る。
func (e *Emitter) Emit(resource apid.EventResource, typ apid.EventType, data any) {
	var raw json.RawMessage
	if r, ok := data.(json.RawMessage); ok {
		raw = append(json.RawMessage(nil), r...)
	} else {
		b, err := json.Marshal(data)
		if err != nil {
			mlog.Error("Event.emit(): failed to marshal data (%s/%s): %s", resource, typ, err)
			b = []byte("null")
		}
		raw = b
	}

	ev := apid.Event{Resource: resource, Type: typ, Data: raw, Time: e.now().UnixMilli()}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.log = append(e.log, ev)
	if len(e.log) > MaxLog {
		e.log = append([]apid.Event(nil), e.log[len(e.log)-MaxLog:]...)
	}

	for _, ch := range e.subs {
		select {
		case ch <- ev:
		default:
			e.dropped.Add(1)
		}
	}
}

// Log は履歴 (最大 100 件, 古い順) のコピーを返す。
func (e *Emitter) Log() []apid.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]apid.Event(nil), e.log...)
}

// OnEvent は購読チャネルを返す。cancel (RemoveListener 相当) で解除しチャネルを close する。
func (e *Emitter) OnEvent(buffer int) (<-chan apid.Event, func()) {
	if buffer < 1 {
		buffer = 1
	}
	ch := make(chan apid.Event, buffer)
	e.mu.Lock()
	id := e.nextSubID
	e.nextSubID++
	e.subs[id] = ch
	e.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			e.mu.Lock()
			delete(e.subs, id)
			close(ch)
			e.mu.Unlock()
		})
	}
}

// Dropped は購読者のバッファ溢れで破棄したイベント数の累計。
func (e *Emitter) Dropped() int64 { return e.dropped.Load() }
