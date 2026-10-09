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

// Package core は Node 版 Mirakurun のグローバル `_` (_.config/_.channel/_.service/
// _.program/_.status/_.tuner/_.job/_.event) を Go の App 構造体に置き換える。
//
// すべてのストアは並行利用可能 (RWMutex) で、時刻・タイマーは注入された job.Clock を使う。
package core

import (
	"sort"
	"sync"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/job"
	"github.com/aki0429/Mirakurun/go/internal/mlog"
)

// ErrorKind は errorCount の集計キー。
type ErrorKind string

const (
	// ErrorUncaughtException は捕捉されなかった例外。
	ErrorUncaughtException ErrorKind = "uncaughtException"
	// ErrorUnhandledRejection は未処理の Promise 拒否。
	ErrorUnhandledRejection ErrorKind = "unhandledRejection"
	// ErrorBufferOverflow は TS バッファ溢れ。
	ErrorBufferOverflow ErrorKind = "bufferOverflow"
	// ErrorTunerDeviceRespawn はチューナーデバイス再起動。
	ErrorTunerDeviceRespawn ErrorKind = "tunerDeviceRespawn"
	// ErrorDecoderRespawn はデコーダ再起動。
	ErrorDecoderRespawn ErrorKind = "decoderRespawn"
)

// タイマー精度サンプルの窓サイズ (Node 版 status.ts と同じ)。
const (
	timerWindow1s    = 60
	timerWindow5s    = 60 * 5
	timerWindow15s   = 60 * 15
	timerTickStartup = 10 * time.Second
	timerTickInitial = 9 * time.Second
)

// timerAccuracy は Node 版 status.timerAccuracy の内部表現 (ns 単位)。
type timerAccuracy struct {
	last int64
	m1   []int64
	m5   []int64
	m15  []int64
}

func newTimerAccuracy() *timerAccuracy {
	return &timerAccuracy{
		m1:  make([]int64, timerWindow1s),
		m5:  make([]int64, timerWindow5s),
		m15: make([]int64, timerWindow15s),
	}
}

// record は 1 サンプルを記録する。Node 版 tick() と同じく
// last = elapsed - 1e9 [ns] とし、各窓を push + shift する。
func (t *timerAccuracy) record(elapsedNs int64) {
	t.last = elapsedNs - 1_000_000_000
	t.m1 = pushShift(t.m1, t.last)
	t.m5 = pushShift(t.m5, t.last)
	t.m15 = pushShift(t.m15, t.last)
}

func pushShift(a []int64, v int64) []int64 {
	copy(a, a[1:])
	a[len(a)-1] = v
	return a
}

// Status は Node 版 status.ts のカウンタ類 + タイマー精度サンプラ。
// TunerDeviceCount はチューナー統合側 (wave 2) が isUsing なデバイス数を返すよう設定する。
type Status struct {
	mu     sync.Mutex
	clock  job.Clock
	log    *mlog.Logger
	rpc    int64
	tsFilt int64
	decode int64
	errs   map[ErrorKind]int64
	epg    map[int]bool
	timer  *timerAccuracy

	// TunerDeviceCount は使用中チューナーデバイス数を返す。nil なら 0。
	TunerDeviceCount func() int
}

// NewStatus は Status を作る。clock は timerAccuracy サンプラに使う。
func NewStatus(clock job.Clock, log *mlog.Logger) *Status {
	if clock == nil {
		clock = job.RealClock{}
	}
	return &Status{
		clock: clock,
		log:   log,
		errs:  map[ErrorKind]int64{},
		epg:   map[int]bool{},
		timer: newTimerAccuracy(),
	}
}

// AddRPC は rpcCount を 1 増やす。
func (s *Status) AddRPC() { s.mu.Lock(); s.rpc++; s.mu.Unlock() }

// AddRPCN は rpcCount を n 増やす。
func (s *Status) AddRPCN(n int64) { s.mu.Lock(); s.rpc += n; s.mu.Unlock() }

// RPCCount は rpcCount を返す。
func (s *Status) RPCCount() int64 { s.mu.Lock(); defer s.mu.Unlock(); return s.rpc }

// AddTSFilter は tsFilter の同時接続数を delta だけ増減する。
func (s *Status) AddTSFilter(delta int64) { s.mu.Lock(); s.tsFilt += delta; s.mu.Unlock() }

// AddDecoder は decoder の同時接続数を delta だけ増減する。
func (s *Status) AddDecoder(delta int64) { s.mu.Lock(); s.decode += delta; s.mu.Unlock() }

// TSFilterCount は tsFilter の現在値を返す。
func (s *Status) TSFilterCount() int64 { s.mu.Lock(); defer s.mu.Unlock(); return s.tsFilt }

// DecoderCount は decoder の現在値を返す。
func (s *Status) DecoderCount() int64 { s.mu.Lock(); defer s.mu.Unlock(); return s.decode }

// AddError は errorCount を 1 増やす。
func (s *Status) AddError(kind ErrorKind) {
	s.mu.Lock()
	s.errs[kind]++
	s.mu.Unlock()
}

// ErrorCount は指定種別の errorCount を返す。
func (s *Status) ErrorCount(kind ErrorKind) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errs[kind]
}

// SetEPGGathering は networkId の EPG 回収中フラグを設定する (Node 版 status.epg[nid])。
func (s *Status) SetEPGGathering(networkId int, gathering bool) {
	s.mu.Lock()
	s.epg[networkId] = gathering
	s.mu.Unlock()
}

// IsEPGGathering は networkId の EPG 回収中フラグを返す。
func (s *Status) IsEPGGathering(networkId int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.epg[networkId] == true
}

// GatheringNetworks は回収中の networkId を昇順で返す
// (JS のオブジェクト整数キーは昇順で列挙されるため)。
func (s *Status) GatheringNetworks() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []int{}
	for nid, on := range s.epg {
		if on {
			out = append(out, nid)
		}
	}
	sort.Ints(out)
	return out
}

// TimerAccuracy は生のサンプル (ns 配列) のコピーを返す (テスト用)。
func (s *Status) TimerAccuracy() (last int64, m1, m5, m15 []int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.timer.last,
		append([]int64(nil), s.timer.m1...),
		append([]int64(nil), s.timer.m5...),
		append([]int64(nil), s.timer.m15...)
}

// StartTimer は Node 版 status.ts のタイマー精度サンプリングを開始する。
// 9 秒後に基準時刻を取り、10 秒後から 1 秒ごとにサンプルを記録する。
func (s *Status) StartTimer() {
	var last time.Time
	s.clock.AfterFunc(timerTickInitial, func() { last = s.clock.Now() })
	var tick func()
	tick = func() {
		now := s.clock.Now()
		if !last.IsZero() {
			s.mu.Lock()
			s.timer.record(now.Sub(last).Nanoseconds())
			s.mu.Unlock()
		}
		last = now
		s.clock.AfterFunc(time.Second, tick)
	}
	s.clock.AfterFunc(timerTickStartup, tick)
}

// recordSampleForTest はテストから直接サンプルを注入する。
func (s *Status) recordSampleForTest(elapsedNs int64) {
	s.mu.Lock()
	s.timer.record(elapsedNs)
	s.mu.Unlock()
}

// minMaxAvgForTest は ns 配列から μs の MinMaxAvg を計算する。
func minMaxAvgForTest(a []int64) apid.MinMaxAvg {
	if len(a) == 0 {
		return apid.MinMaxAvg{}
	}
	var sum int64
	mn := a[0]
	mx := a[0]
	for _, v := range a {
		sum += v
		if v < mn {
			mn = v
		}
		if v > mx {
			mx = v
		}
	}
	return apid.MinMaxAvg{
		Avg: float64(sum) / float64(len(a)) / 1000.0,
		Min: float64(mn) / 1000.0,
		Max: float64(mx) / 1000.0,
	}
}
