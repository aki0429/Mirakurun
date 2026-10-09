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

// Package job は Node 版 Job.ts (ジョブキュー + cron 風スケジューラ) の移植。
//
// 状態遷移: queued → standby (readyFn 評価中 / 実行枠待ち) → running → finished。
// Node 版の非同期処理 (await) は goroutine に置き換え、状態は Manager.mu で保護する。
// 時刻・タイマーはすべて注入された Clock を使うため、FakeClock で決定的にテストできる。
package job

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/event"
	"github.com/aki0429/Mirakurun/go/internal/mlog"
)

// DefaultMaxHistory は完了ジョブ履歴の保持数 (Node 版と同じ 50)。
const DefaultMaxHistory = 50

// Fn はジョブ本体。ctx は Abort / Close でキャンセルされる (Node 版の AbortSignal 相当)。
type Fn func(ctx context.Context) error

// ReadyFn は実行前の準備確認。false (またはエラー) を返すとジョブはスキップされる。
type ReadyFn func(ctx context.Context) (bool, error)

// Item はジョブ定義 (Node 版 JobItem)。ポインタのフィールドは未指定 (undefined) を表現するため。
type Item struct {
	Key     string
	Name    string
	Fn      Fn
	ReadyFn ReadyFn
	// IsRerunnable は再実行可能かどうか。
	IsRerunnable *bool
	// RetryOnAbort は abort 時にリトライするかどうか。
	RetryOnAbort *bool
	// RetryOnFail は失敗時にリトライするかどうか。
	RetryOnFail *bool
	// RetryMax は最大リトライ回数 (未指定は 0)。
	RetryMax *int
	// RetryDelay はリトライ間隔 (ミリ秒, 最低 1000)。
	RetryDelay *int
}

// ScheduleItem はスケジュール定義 (Node 版 ScheduleItem)。
type ScheduleItem struct {
	Key string
	Job Item
	// Schedule は `m h dom mon dow` 形式。
	Schedule string
}

// Options は Manager の生成オプション。
type Options struct {
	// MaxRunning は同時実行数。0 なら DefaultMaxRunning(runtime CPU 数) を呼び出し側で設定すること
	// (config.jobMaxRunning が優先)。0 のままなら 1 として扱う。
	MaxRunning int
	// MaxStandby は同時に ready チェックを行う最大数。0 なら 1。
	MaxStandby int
	Clock      Clock
	Event      *event.Emitter
	Logger     *mlog.Logger
}

// DefaultMaxRunning は Node 版 Math.min(100, Math.max(1, Math.floor(cpus / 2)))。
func DefaultMaxRunning(cpus int) int { return min(100, max(1, cpus/2)) }

// DefaultMaxStandby は Node 版 Math.min(100, Math.max(1, Math.floor(cpus - 1)))。
func DefaultMaxStandby(cpus int) int { return min(100, max(1, cpus-1)) }

// jobState は 1 つのジョブ実行インスタンス (Node 版 Queued/Running/FinishedJobItem)。
type jobState struct {
	Item
	id          string
	retryCount  int
	createdAt   int64
	startedAt   int64
	finishedAt  int64
	ctx         context.Context
	cancel      context.CancelFunc
	aborted     bool
	abortReason string
	hasFailed   bool
	err         error
}

// Manager はジョブキューとスケジュールを管理する。並行利用可能。
type Manager struct {
	mu         sync.Mutex
	clock      Clock
	ev         *event.Emitter
	log        *mlog.Logger
	maxRunning int
	maxStandby int
	maxHistory int

	idPrefix  string
	idCounter int

	queued    []*jobState
	standby   []*jobState
	running   []*jobState
	finished  []*jobState
	schedules []*ScheduleItem

	scheduleTimer   Timer
	queueCheckTimer Timer
	closed          bool
	closeCtx        context.Context
	closeFn         context.CancelFunc
}

// New は Manager を作り、1 分ごとのスケジュール確認を開始する。
func New(opts Options) *Manager {
	m := &Manager{
		clock:      opts.Clock,
		ev:         opts.Event,
		log:        opts.Logger,
		maxRunning: opts.MaxRunning,
		maxStandby: opts.MaxStandby,
		maxHistory: DefaultMaxHistory,
	}
	if m.clock == nil {
		m.clock = RealClock{}
	}
	if m.ev == nil {
		m.ev = event.New(event.Options{Now: m.clock.Now})
	}
	if m.log == nil {
		m.log = mlog.Default()
	}
	if m.maxRunning < 1 {
		m.maxRunning = 1
	}
	if m.maxStandby < 1 {
		m.maxStandby = 1
	}
	m.closeCtx, m.closeFn = context.WithCancel(context.Background())
	m.idPrefix = formatBase36(m.clock.Now().UnixMilli()) + "."

	m.mu.Lock()
	m.armScheduleTimerLocked()
	m.mu.Unlock()
	return m
}

// formatBase36 は JavaScript の n.toString(36).toUpperCase() 相当。
func formatBase36(n int64) string { return strings.ToUpper(strconv.FormatInt(n, 36)) }

// MaxRunning は同時実行数を返す。
func (m *Manager) MaxRunning() int { return m.maxRunning }

// MaxStandby は同時 ready チェック数を返す。
func (m *Manager) MaxStandby() int { return m.maxStandby }

// MaxHistory は履歴保持数を返す。
func (m *Manager) MaxHistory() int { return m.maxHistory }

// Schedules はスケジュール一覧 (追加順) を返す。
func (m *Manager) Schedules() []apid.JobScheduleItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]apid.JobScheduleItem, 0, len(m.schedules))
	for _, s := range m.schedules {
		out = append(out, scheduleToJSON(s))
	}
	return out
}

// Jobs は queued, standby, running, finished (新しい順) の順でジョブ一覧を返す。
func (m *Manager) Jobs() []apid.JobItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]apid.JobItem, 0, len(m.queued)+len(m.standby)+len(m.running)+len(m.finished))
	for _, j := range m.queued {
		out = append(out, jobToJSON("queued", j))
	}
	for _, j := range m.standby {
		out = append(out, jobToJSON("standby", j))
	}
	for _, j := range m.running {
		out = append(out, jobToJSON("running", j))
	}
	for _, j := range m.finished {
		out = append(out, jobToJSON("finished", j))
	}
	return out
}

// Close はスケジュール確認を止め、実行中のジョブを abort する。
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	if m.scheduleTimer != nil {
		m.scheduleTimer.Stop()
	}
	if m.queueCheckTimer != nil {
		m.queueCheckTimer.Stop()
		m.queueCheckTimer = nil
	}
	for _, j := range m.running {
		m.abortLocked(j, "")
	}
	m.closeFn()
}

// Add はジョブをキューに追加する。同じ key のジョブが queued / standby / running にあれば無視する。
func (m *Manager) Add(item Item) { m.add(item, 0) }

func (m *Manager) add(item Item, retryCount int) {
	m.log.Info("Job#add() adding \"%s\"", item.Key)

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, list := range [][]*jobState{m.queued, m.standby, m.running} {
		for _, j := range list {
			if j.Key == item.Key {
				m.log.Warn("Job#add() ignore adding \"%s\" because already in the queue or running.", item.Key)
				return
			}
		}
	}

	m.idCounter++
	ctx, cancel := context.WithCancel(m.closeCtx)
	j := &jobState{
		Item:       item,
		id:         m.idPrefix + strconv.Itoa(m.idCounter),
		retryCount: retryCount,
		createdAt:  m.clock.Now().UnixMilli(),
		ctx:        ctx,
		cancel:     cancel,
	}
	m.queued = append(m.queued, j)

	m.ev.Emit(apid.EventResourceJob, apid.EventTypeCreate, jobToJSON("queued", j))

	m.checkQueueLocked()
}

// Rerun は完了済みジョブを再実行する (isRerunnable が true のときのみ)。
func (m *Manager) Rerun(id string) bool {
	m.mu.Lock()
	var target *jobState
	for _, j := range m.finished {
		if j.id == id {
			target = j
			break
		}
	}
	m.mu.Unlock()

	if target == nil {
		m.log.Warn("Job#rerun() \"%s\" not found in finished jobs", id)
		return false
	}
	m.log.Info("Job#rerun() rerun requested \"%s\" (id: %s)", target.Key, target.id)
	if target.IsRerunnable == nil || !*target.IsRerunnable {
		m.log.Warn("Job#rerun() \"%s\" (id: %s) is not rerunnable", target.Key, target.id)
		return false
	}
	m.add(Item{
		Key:          target.Key,
		Name:         target.Name,
		Fn:           target.Fn,
		ReadyFn:      target.ReadyFn,
		IsRerunnable: apid.Ptr(true),
		RetryOnAbort: apid.Ptr(false),
		RetryOnFail:  apid.Ptr(false),
		RetryMax:     apid.Ptr(0),
	}, 0)
	return true
}

// Abort は running / standby / queued のジョブを abort する。
func (m *Manager) Abort(id, reason string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	type cand struct {
		j      *jobState
		status string
	}
	var cands []cand
	for _, j := range m.running {
		cands = append(cands, cand{j, "running"})
	}
	for _, j := range m.standby {
		cands = append(cands, cand{j, "standby"})
	}
	for _, j := range m.queued {
		cands = append(cands, cand{j, "queued"})
	}
	for _, c := range cands {
		if c.j.id == id && !c.j.aborted {
			m.abortLocked(c.j, reason)
			m.log.Info("Job#abort() abort requested \"%s\" (id: %s)", c.j.Key, c.j.id)
			m.ev.Emit(apid.EventResourceJob, apid.EventTypeUpdate, jobToJSON(c.status, c.j))
			return true
		}
	}
	m.log.Warn("Job#abort() \"%s\" not found in running or queued jobs", id)
	return false
}

// abortLocked は AbortController.abort(reason) 相当 (既に abort 済みなら何もしない)。
func (m *Manager) abortLocked(j *jobState, reason string) {
	if j.aborted {
		return
	}
	j.aborted = true
	j.abortReason = reason
	j.cancel()
}

// AddSchedule はスケジュールを追加する。同じ key があれば警告して無視 (nil を返す)。
// 書式が不正ならエラーを返す (Node 版は throw)。
func (m *Manager) AddSchedule(s ScheduleItem) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, x := range m.schedules {
		if x.Key == s.Key {
			m.log.Warn("Job#addSchedule() ignore adding \"%s\" because already in the schedule.", s.Key)
			return nil
		}
	}
	m.log.Info("Job#addSchedule() adding \"%s\"", s.Key)
	if !IsValidCronExpression(s.Schedule) {
		return fmt.Errorf("Invalid schedule format: %s", s.Schedule)
	}
	sc := s
	m.schedules = append(m.schedules, &sc)
	m.ev.Emit(apid.EventResourceJobSchedule, apid.EventTypeCreate, scheduleToJSON(&sc))
	return nil
}

// RunSchedule はスケジュールのジョブを即時キューに追加する。見つからなければ false。
func (m *Manager) RunSchedule(key string) bool {
	m.mu.Lock()
	var found *ScheduleItem
	for _, s := range m.schedules {
		if s.Key == key {
			found = s
			break
		}
	}
	m.mu.Unlock()
	if found == nil {
		m.log.Warn("Job#runSchedule() \"%s\" not found in schedule", key)
		return false
	}
	m.Add(found.Job)
	return true
}

// armScheduleTimerLocked は 60 秒後のスケジュール確認を予約する (setInterval 相当)。
func (m *Manager) armScheduleTimerLocked() {
	if m.closed {
		return
	}
	m.scheduleTimer = m.clock.AfterFunc(time.Minute, func() {
		m.mu.Lock()
		m.armScheduleTimerLocked()
		m.mu.Unlock()
		m.checkSchedule()
	})
}

func (m *Manager) checkSchedule() {
	now := m.clock.Now()

	m.mu.Lock()
	schedules := append([]*ScheduleItem(nil), m.schedules...)
	m.mu.Unlock()

	var invalid []*ScheduleItem
	for _, s := range schedules {
		ok, err := MatchCronExpression(s.Schedule, now)
		if err != nil {
			m.log.Error("Job#_checkSchedule() %s err=%s", s.Key, err.Error())
			invalid = append(invalid, s)
			continue
		}
		if ok {
			m.add(s.Job, 0)
		}
	}

	if len(invalid) > 0 {
		m.mu.Lock()
		kept := m.schedules[:0]
		for _, s := range m.schedules {
			bad := false
			for _, x := range invalid {
				if x == s {
					bad = true
				}
			}
			if !bad {
				kept = append(kept, s)
			}
		}
		m.schedules = kept
		m.mu.Unlock()
	}
}

// checkQueueLocked は Node 版 _checkQueue。
//
// Node 版は配列を for...of で回しながら _checkReady 内で splice するため、
// 1 回の確認で先頭から 1 つおきのジョブだけが standby に移る (残りは 1 秒後の再確認で処理)。
// タイミングを合わせるため、この挙動もそのまま再現している。
func (m *Manager) checkQueueLocked() {
	m.log.Debug("Job#_checkQueue() queue=%d standby=%d running=%d", len(m.queued), len(m.standby), len(m.running))

	if len(m.standby) >= m.maxStandby {
		m.log.Debug("Job#_checkQueue() standby is full [skip]")
		return
	}
	if len(m.queued) == 0 {
		m.log.Debug("Job#_checkQueue() queue is empty [skip]")
		return
	}
	for i := 0; i < len(m.queued); i++ {
		if len(m.standby) >= m.maxStandby {
			break
		}
		m.checkReadyLocked(m.queued[i])
	}
}

// checkReadyLocked は Node 版 _checkReady の同期部分 (queued → standby) を行い、
// 残り (readyFn, 実行枠待ち, 実行) を goroutine で続ける。
func (m *Manager) checkReadyLocked(j *jobState) {
	m.log.Debug("Job#_checkReady() checking \"%s\" (id: %s)", j.Key, j.id)

	idx := indexOf(m.queued, j)
	if idx == -1 {
		m.log.Error("Job#_checkReady() \"%s\" (id: %s) not found in queue", j.Key, j.id)
		return
	}
	m.queued = append(m.queued[:idx], m.queued[idx+1:]...)
	m.standby = append(m.standby, j)

	m.ev.Emit(apid.EventResourceJob, apid.EventTypeUpdate, jobToJSON("standby", j))

	go m.continueReady(j)
}

func (m *Manager) continueReady(j *jobState) {
	skip := false
	if j.ReadyFn != nil {
		ok, err := j.ReadyFn(j.ctx)
		if err != nil {
			m.log.Error("Job#_checkReady() \"%s\" readyFn error: %s", j.Key, err.Error())
			skip = true
		} else {
			skip = !ok
		}
	}

	// 実行枠が空くまで 1 秒ごとに確認する (skip の場合は待たない)。
	// 枠の確認と running への追加は同じロック内で行い、取り合いを防ぐ。
	waited := false
	for {
		m.mu.Lock()
		if skip || len(m.running) < m.maxRunning || m.closed {
			break
		}
		m.mu.Unlock()
		if !waited {
			m.log.Debug("Job#_checkReady() running is full [wait]")
			waited = true
		}
		if err := Sleep(m.closeCtx, m.clock, time.Second); err != nil {
			// Close 済み: 次のループで抜ける
			continue
		}
	}
	// ここではロック保持中
	if idx := indexOf(m.standby, j); idx != -1 {
		m.standby = append(m.standby[:idx], m.standby[idx+1:]...)
	}
	now := m.clock.Now().UnixMilli()
	j.startedAt = now

	run := false
	if skip || j.aborted {
		if skip {
			m.abortLocked(j, "skipped")
			m.log.Debug("Job#_checkReady() skipped \"%s\" (id: %s) due to readyFn returning false", j.Key, j.id)
		} else {
			m.log.Debug("Job#_checkReady() aborted \"%s\" (id: %s) due to abort signal", j.Key, j.id)
		}
		m.finishLocked(j, nil)
	} else {
		m.log.Debug("Job#_run() starting \"%s\" (id: %s)", j.Key, j.id)
		m.running = append(m.running, j)
		m.ev.Emit(apid.EventResourceJob, apid.EventTypeUpdate, jobToJSON("running", j))
		run = true
	}

	m.log.Debug("Job#_checkReady() done \"%s\" (id: %s)", j.Key, j.id)
	m.armQueueCheckLocked()
	m.mu.Unlock()

	if run {
		m.runJob(j)
	}
}

// armQueueCheckLocked は 1 秒後の _checkQueue を (既存のものを置き換えて) 予約する。
func (m *Manager) armQueueCheckLocked() {
	if m.closed {
		return
	}
	if m.queueCheckTimer != nil {
		m.queueCheckTimer.Stop()
	}
	m.queueCheckTimer = m.clock.AfterFunc(time.Second, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.checkQueueLocked()
	})
}

func (m *Manager) runJob(j *jobState) {
	var err error
	if j.Fn == nil {
		err = errors.New("job fn is not defined")
	} else {
		err = safeCall(j.Fn, j.ctx)
	}

	m.mu.Lock()
	if idx := indexOf(m.running, j); idx != -1 {
		m.running = append(m.running[:idx], m.running[idx+1:]...)
	}
	m.finishLocked(j, err)
	m.mu.Unlock()

	m.log.Debug("Job#_run() done \"%s\" (id: %s)", j.Key, j.id)

	if err != nil {
		go m.retryJob(j)
	}
}

// safeCall は panic をエラーに変換する (Node 版で throw されたのと同じ扱いにする)。
func safeCall(fn Fn, ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	return fn(ctx)
}

// finishLocked は Node 版 _finishJob。err が nil なら成功扱い。
func (m *Manager) finishLocked(j *jobState, err error) {
	j.finishedAt = m.clock.Now().UnixMilli()
	j.hasFailed = err != nil
	j.err = err

	m.finished = append([]*jobState{j}, m.finished...)
	if len(m.finished) > m.maxHistory {
		removed := m.finished[m.maxHistory:]
		m.finished = append([]*jobState(nil), m.finished[:m.maxHistory]...)
		for _, r := range removed {
			// Node 版: remove ではなく update (isRerunnable=false) で通知する
			jj := jobToJSON("finished", r)
			jj.IsRerunnable = apid.Ptr(false)
			m.ev.Emit(apid.EventResourceJob, apid.EventTypeUpdate, jj)
		}
	}

	status := "completed"
	if err != nil {
		status = "failed"
	}
	abortMsg := ""
	if j.aborted {
		abortMsg = " (aborted)"
	}
	m.log.Info("Job#_finishJob() \"%s\" %s%s in %dms", j.Key, status, abortMsg, j.finishedAt-j.startedAt)

	m.ev.Emit(apid.EventResourceJob, apid.EventTypeUpdate, jobToJSON("finished", j))
}

// retryJob は Node 版 _retryJob。
func (m *Manager) retryJob(j *jobState) {
	m.mu.Lock()
	aborted := j.aborted
	m.mu.Unlock()

	shouldRetryOnAbort := j.RetryOnAbort != nil && *j.RetryOnAbort && aborted
	shouldRetryOnFail := j.RetryOnFail != nil && *j.RetryOnFail && !aborted
	retryMax := 0
	if j.RetryMax != nil {
		retryMax = *j.RetryMax
	}
	next := j.retryCount + 1
	if !(shouldRetryOnAbort || shouldRetryOnFail) || next > retryMax {
		return
	}
	delay := 1000
	if j.RetryDelay != nil && *j.RetryDelay > delay {
		delay = *j.RetryDelay
	}
	m.log.Warn("Job#_handleJobError() \"%s\" will retry (%d/%d) after %dms", j.Key, next, retryMax, delay)

	if err := Sleep(m.closeCtx, m.clock, time.Duration(delay)*time.Millisecond); err != nil {
		return
	}
	m.add(Item{
		Key:          j.Key,
		Name:         j.Name,
		Fn:           j.Fn,
		ReadyFn:      j.ReadyFn,
		IsRerunnable: j.IsRerunnable,
		RetryOnAbort: j.RetryOnAbort,
		RetryOnFail:  j.RetryOnFail,
		RetryMax:     j.RetryMax,
		RetryDelay:   j.RetryDelay,
	}, next)
}

func indexOf(list []*jobState, j *jobState) int {
	for i, x := range list {
		if x == j {
			return i
		}
	}
	return -1
}

func scheduleToJSON(s *ScheduleItem) apid.JobScheduleItem {
	return apid.JobScheduleItem{
		Key:      s.Key,
		Schedule: s.Schedule,
		Job:      apid.JobScheduleJob{Key: s.Job.Key, Name: s.Job.Name},
	}
}

// jobToJSON は Node 版 jobToJSON。呼び出し側でロックを保持していること。
func jobToJSON(status string, j *jobState) apid.JobItem {
	out := apid.JobItem{
		Key:          j.Key,
		Name:         j.Name,
		ID:           j.id,
		Status:       status,
		RetryCount:   j.retryCount,
		IsAborting:   j.aborted,
		CreatedAt:    j.createdAt,
		IsRerunnable: j.IsRerunnable,
		RetryOnAbort: j.RetryOnAbort,
		RetryOnFail:  j.RetryOnFail,
		RetryMax:     j.RetryMax,
		RetryDelay:   j.RetryDelay,
	}
	out.UpdatedAt = j.createdAt
	out.Duration = apid.Ptr(int64(0))
	if j.startedAt != 0 {
		out.StartedAt = apid.Ptr(j.startedAt)
		out.UpdatedAt = j.startedAt
	}
	if status == "finished" {
		out.FinishedAt = apid.Ptr(j.finishedAt)
		out.UpdatedAt = j.finishedAt
		out.Duration = apid.Ptr(j.finishedAt - j.startedAt)
		out.HasAborted = apid.Ptr(j.aborted)
		out.HasSkipped = apid.Ptr(j.aborted && j.abortReason == "skipped")
		out.HasFailed = apid.Ptr(j.hasFailed)
		if j.err != nil {
			out.Error = apid.Ptr(j.err.Error())
		}
	}
	return out
}
