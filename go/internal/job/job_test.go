package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/event"
)

// 2026-10-09T00:00:30+09:00 (分境界の 30 秒後に起動した想定)
var testStart = time.Date(2026, 10, 9, 0, 0, 30, 0, time.FixedZone("JST", 9*3600))

type harness struct {
	t     *testing.T
	clock *FakeClock
	ev    *event.Emitter
	m     *Manager
	evCh  <-chan apid.Event
}

func newHarness(t *testing.T, opts Options) *harness {
	t.Helper()
	clock := NewFakeClock(testStart)
	ev := event.New(event.Options{Now: clock.Now})
	ch, cancel := ev.OnEvent(100000)
	t.Cleanup(cancel)
	opts.Clock = clock
	opts.Event = ev
	m := New(opts)
	t.Cleanup(m.Close)
	return &harness{t: t, clock: clock, ev: ev, m: m, evCh: ch}
}

// drainEvents は届いているイベントを全部取り出す。
func (h *harness) drainEvents() []apid.Event {
	var out []apid.Event
	for {
		select {
		case e := <-h.evCh:
			out = append(out, e)
		default:
			return out
		}
	}
}

func (h *harness) jobByID(id string) *apid.JobItem {
	for _, j := range h.m.Jobs() {
		if j.ID == id {
			jj := j
			return &jj
		}
	}
	return nil
}

func (h *harness) waitStatus(key, status string) apid.JobItem {
	h.t.Helper()
	var found apid.JobItem
	waitFor(h.t, func() bool {
		for _, j := range h.m.Jobs() {
			if j.Key == key && j.Status == status {
				found = j
				return true
			}
		}
		return false
	})
	return found
}

func statuses(evs []apid.Event, key string) []string {
	var out []string
	for _, e := range evs {
		if e.Resource != apid.EventResourceJob {
			continue
		}
		var j apid.JobItem
		_ = json.Unmarshal(e.Data, &j)
		if j.Key == key {
			out = append(out, string(e.Type)+":"+j.Status)
		}
	}
	return out
}

func TestDefaultLimitsFollowCPUCount(t *testing.T) {
	if got := DefaultMaxRunning(8); got != 4 {
		t.Fatal(got)
	}
	if got := DefaultMaxRunning(1); got != 1 {
		t.Fatal(got)
	}
	if got := DefaultMaxRunning(1000); got != 100 {
		t.Fatal(got)
	}
	if got := DefaultMaxStandby(8); got != 7 {
		t.Fatal(got)
	}
	if got := DefaultMaxStandby(1); got != 1 {
		t.Fatal(got)
	}
	if got := DefaultMaxStandby(500); got != 100 {
		t.Fatal(got)
	}
	h := newHarness(t, Options{MaxRunning: 3, MaxStandby: 5})
	if h.m.MaxRunning() != 3 || h.m.MaxStandby() != 5 || h.m.MaxHistory() != 50 {
		t.Fatal("explicit limits not applied")
	}
}

func TestJobIDUsesBase36UpperStartupMillisAndCounter(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 1, MaxStandby: 1})
	block := make(chan struct{})
	defer close(block)
	h.m.Add(Item{Key: "a", Name: "A", Fn: func(ctx context.Context) error { <-block; return nil }})
	h.m.Add(Item{Key: "b", Name: "B", Fn: func(ctx context.Context) error { return nil }})
	prefix := formatBase36(testStart.UnixMilli()) + "."
	ids := []string{}
	for _, j := range h.m.Jobs() {
		ids = append(ids, j.ID)
	}
	if len(ids) != 2 {
		t.Fatalf("jobs %v", ids)
	}
	want := map[string]bool{prefix + "1": true, prefix + "2": true}
	for _, id := range ids {
		if !want[id] {
			t.Fatalf("unexpected id %q (prefix %q)", id, prefix)
		}
	}
}

func TestFormatBase36MatchesJavaScript(t *testing.T) {
	// node -e 'console.log((1791400000123).toString(36).toUpperCase())' => MUYHB2IJ
	if got := formatBase36(1791400000123); got != "MUYHB2IJ" {
		t.Fatalf("got %s", got)
	}
}

func TestSuccessfulJobLifecycleEventsAndFinishedJSON(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 2, MaxStandby: 2})
	ran := make(chan struct{})
	h.m.Add(Item{Key: "k", Name: "Job K", IsRerunnable: apid.Ptr(true), Fn: func(ctx context.Context) error {
		h.clock.Advance(1500 * time.Millisecond) // 実行時間を 1500ms にする
		close(ran)
		return nil
	}})
	<-ran
	fin := h.waitStatus("k", "finished")
	if got := statuses(h.drainEvents(), "k"); strings.Join(got, ",") != "create:queued,update:standby,update:running,update:finished" {
		t.Fatalf("events %v", got)
	}
	if fin.Duration == nil || *fin.Duration != 1500 || fin.HasFailed == nil || *fin.HasFailed || fin.HasAborted == nil || *fin.HasAborted || fin.IsAborting {
		b, _ := json.Marshal(fin)
		t.Fatalf("finished %s", b)
	}
	if fin.UpdatedAt != *fin.FinishedAt || *fin.StartedAt != fin.CreatedAt || fin.Error != nil {
		b, _ := json.Marshal(fin)
		t.Fatalf("times %s", b)
	}
	if fin.IsRerunnable == nil || !*fin.IsRerunnable || fin.RetryOnFail != nil {
		t.Fatal("optional fields should mirror the job item")
	}
}

func TestQueuedJobJSONHasZeroDuration(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 1, MaxStandby: 1})
	h.m.Add(Item{Key: "k", Name: "K", Fn: func(ctx context.Context) error { return nil }})
	evs := h.drainEvents()
	var j apid.JobItem
	_ = json.Unmarshal(evs[0].Data, &j)
	if j.Status != "queued" || j.Duration == nil || *j.Duration != 0 || j.UpdatedAt != j.CreatedAt || j.StartedAt != nil {
		t.Fatalf("queued json %s", evs[0].Data)
	}
}

func TestDuplicateKeyIsIgnoredUntilFinished(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 1, MaxStandby: 1})
	block := make(chan struct{})
	var calls atomic.Int32
	fn := func(ctx context.Context) error { calls.Add(1); <-block; return nil }
	h.m.Add(Item{Key: "dup", Name: "D", Fn: fn})
	h.waitStatus("dup", "running")
	h.m.Add(Item{Key: "dup", Name: "D", Fn: fn})
	if n := len(h.m.Jobs()); n != 1 {
		t.Fatalf("jobs %d", n)
	}
	close(block)
	h.waitStatus("dup", "finished")
	h.m.Add(Item{Key: "dup", Name: "D", Fn: func(ctx context.Context) error { return nil }})
	waitFor(t, func() bool { return len(h.m.Jobs()) == 2 })
}

func TestMaxRunningMakesStandbyWaitAndPollEverySecond(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 1, MaxStandby: 2})
	block := make(chan struct{})
	h.m.Add(Item{Key: "a", Name: "A", Fn: func(ctx context.Context) error { <-block; return nil }})
	h.waitStatus("a", "running")
	var bRan atomic.Bool
	h.m.Add(Item{Key: "b", Name: "B", Fn: func(ctx context.Context) error { bRan.Store(true); return nil }})
	h.waitStatus("b", "standby")
	// 実行枠が空くまで b は standby のまま
	h.clock.Advance(3 * time.Second)
	if bRan.Load() {
		t.Fatal("b must not run while a is running")
	}
	close(block)
	h.waitStatus("a", "finished")
	// 1 秒ポーリングなので時計を進めると実行される
	waitFor(t, func() bool { h.clock.Advance(time.Second); return bRan.Load() })
	h.waitStatus("b", "finished")
}

func TestMaxStandbyLimitsReadyChecksAndQueueIsRecheckedAfterOneSecond(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 5, MaxStandby: 1})
	release := make(chan bool)
	ready := func(ctx context.Context) (bool, error) { return <-release, nil }
	h.m.Add(Item{Key: "a", Name: "A", ReadyFn: ready, Fn: func(ctx context.Context) error { return nil }})
	h.waitStatus("a", "standby")
	h.m.Add(Item{Key: "b", Name: "B", ReadyFn: func(ctx context.Context) (bool, error) { return true, nil }, Fn: func(ctx context.Context) error { return nil }})
	if j := h.waitStatus("b", "queued"); j.Key != "b" {
		t.Fatal("b should stay queued")
	}
	release <- true
	h.waitStatus("a", "finished")
	// _checkQueue は _checkReady 完了の 1 秒後
	waitFor(t, func() bool { return h.clock.PendingTimers() > 0 })
	h.clock.Advance(time.Second)
	h.waitStatus("b", "finished")
}

func TestReadyFnFalseSkipsJob(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 1, MaxStandby: 1})
	var ran atomic.Bool
	h.m.Add(Item{Key: "s", Name: "S", ReadyFn: func(ctx context.Context) (bool, error) { return false, nil }, Fn: func(ctx context.Context) error { ran.Store(true); return nil }})
	fin := h.waitStatus("s", "finished")
	if ran.Load() || !*fin.HasAborted || !*fin.HasSkipped || *fin.HasFailed || !fin.IsAborting {
		b, _ := json.Marshal(fin)
		t.Fatalf("skipped %s ran=%v", b, ran.Load())
	}
}

func TestReadyFnErrorSkipsJob(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 1, MaxStandby: 1})
	h.m.Add(Item{Key: "s", Name: "S", ReadyFn: func(ctx context.Context) (bool, error) { return true, errors.New("boom") }, Fn: func(ctx context.Context) error { return nil }})
	fin := h.waitStatus("s", "finished")
	if !*fin.HasSkipped {
		t.Fatal("readyFn error must skip")
	}
}

func TestAbortRunningJobCancelsContextAndEmitsUpdate(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 1, MaxStandby: 1})
	h.m.Add(Item{Key: "r", Name: "R", Fn: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }})
	j := h.waitStatus("r", "running")
	h.drainEvents()
	if !h.m.Abort(j.ID, "user") {
		t.Fatal("abort should succeed")
	}
	if h.m.Abort(j.ID, "again") {
		t.Fatal("second abort should return false")
	}
	fin := h.waitStatus("r", "finished")
	evs := statuses(h.drainEvents(), "r")
	if len(evs) < 2 || evs[0] != "update:running" || evs[len(evs)-1] != "update:finished" {
		t.Fatalf("events %v", evs)
	}
	if !*fin.HasAborted || *fin.HasSkipped || !*fin.HasFailed || fin.Error == nil || *fin.Error != "context canceled" {
		b, _ := json.Marshal(fin)
		t.Fatalf("aborted %s", b)
	}
	if h.m.Abort("nope", "x") {
		t.Fatal("unknown id")
	}
}

func TestAbortQueuedJobFinishesWithoutRunning(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 1, MaxStandby: 1})
	release := make(chan bool)
	h.m.Add(Item{Key: "a", Name: "A", ReadyFn: func(ctx context.Context) (bool, error) { return <-release, nil }, Fn: func(ctx context.Context) error { return nil }})
	h.waitStatus("a", "standby")
	var ran atomic.Bool
	h.m.Add(Item{Key: "q", Name: "Q", Fn: func(ctx context.Context) error { ran.Store(true); return nil }})
	q := h.waitStatus("q", "queued")
	if !h.m.Abort(q.ID, "user") {
		t.Fatal("abort queued")
	}
	if j := h.jobByID(q.ID); !j.IsAborting {
		t.Fatal("queued job should be aborting")
	}
	release <- true
	h.waitStatus("a", "finished")
	waitFor(t, func() bool { h.clock.Advance(time.Second); return h.jobByID(q.ID).Status == "finished" })
	fin := h.jobByID(q.ID)
	if ran.Load() || !*fin.HasAborted || *fin.HasSkipped || *fin.HasFailed {
		b, _ := json.Marshal(fin)
		t.Fatalf("queued abort %s", b)
	}
}

func TestRetryOnFailRequeuesWithIncrementedRetryCountAfterDelay(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 1, MaxStandby: 1})
	var calls atomic.Int32
	h.m.Add(Item{Key: "f", Name: "F", RetryOnFail: apid.Ptr(true), RetryMax: apid.Ptr(2), RetryDelay: apid.Ptr(500),
		Fn: func(ctx context.Context) error { calls.Add(1); return errors.New("fail") }})
	waitFor(t, func() bool { return calls.Load() == 1 && h.clock.PendingTimers() > 0 })
	// retryDelay は最低 1000ms
	h.clock.Advance(999 * time.Millisecond)
	if n := len(h.m.Jobs()); n != 1 {
		t.Fatalf("retried too early: %d jobs", n)
	}
	waitFor(t, func() bool { h.clock.Advance(time.Millisecond * 100); return calls.Load() == 3 })
	// 3 回目 (retryCount=2) の後はもうリトライしない
	h.clock.Advance(10 * time.Second)
	if calls.Load() != 3 {
		t.Fatalf("calls %d", calls.Load())
	}
	counts := map[int]bool{}
	for _, j := range h.m.Jobs() {
		counts[j.RetryCount] = true
		if j.Status != "finished" || !*j.HasFailed || *j.Error != "fail" {
			t.Fatalf("job %+v", j)
		}
	}
	if len(counts) != 3 || !counts[0] || !counts[1] || !counts[2] {
		t.Fatalf("retry counts %v", counts)
	}
}

func TestRetryOnFailDoesNotRetryAbortedJobs(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 1, MaxStandby: 1})
	var calls atomic.Int32
	h.m.Add(Item{Key: "f", Name: "F", RetryOnFail: apid.Ptr(true), RetryMax: apid.Ptr(5),
		Fn: func(ctx context.Context) error { calls.Add(1); <-ctx.Done(); return ctx.Err() }})
	j := h.waitStatus("f", "running")
	h.m.Abort(j.ID, "x")
	h.waitStatus("f", "finished")
	h.clock.Advance(time.Minute)
	if calls.Load() != 1 {
		t.Fatalf("calls %d", calls.Load())
	}
}

func TestRetryOnAbortRetriesAbortedJob(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 1, MaxStandby: 1})
	var calls atomic.Int32
	h.m.Add(Item{Key: "a", Name: "A", RetryOnAbort: apid.Ptr(true), RetryMax: apid.Ptr(1),
		Fn: func(ctx context.Context) error {
			if calls.Add(1) == 1 {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}})
	j := h.waitStatus("a", "running")
	h.m.Abort(j.ID, "x")
	waitFor(t, func() bool { h.clock.Advance(100 * time.Millisecond); return calls.Load() == 2 })
}

func TestRerunRequiresRerunnable(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 1, MaxStandby: 1})
	var calls atomic.Int32
	fn := func(ctx context.Context) error { calls.Add(1); return nil }
	h.m.Add(Item{Key: "n", Name: "N", Fn: fn})
	n := h.waitStatus("n", "finished")
	if h.m.Rerun(n.ID) {
		t.Fatal("not rerunnable")
	}
	h.m.Add(Item{Key: "r", Name: "R", IsRerunnable: apid.Ptr(true), RetryOnFail: apid.Ptr(true), RetryMax: apid.Ptr(9), Fn: fn})
	r := h.waitStatus("r", "finished")
	if !h.m.Rerun(r.ID) {
		t.Fatal("rerunnable")
	}
	waitFor(t, func() bool { return calls.Load() == 3 })
	var rerun apid.JobItem
	waitFor(t, func() bool {
		for _, j := range h.m.Jobs() {
			if j.Key == "r" && j.ID != r.ID && j.Status == "finished" {
				rerun = j
				return true
			}
		}
		return false
	})
	if *rerun.RetryOnFail || *rerun.RetryMax != 0 || *rerun.RetryOnAbort || !*rerun.IsRerunnable {
		b, _ := json.Marshal(rerun)
		t.Fatalf("rerun flags %s", b)
	}
	if h.m.Rerun("missing") {
		t.Fatal("missing id")
	}
}

func TestHistoryKeepsNewestFiftyAndEmitsUpdateForRemoved(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 1, MaxStandby: 1})
	for i := 0; i < 51; i++ {
		key := fmt.Sprintf("h%d", i)
		h.m.Add(Item{Key: key, Name: key, IsRerunnable: apid.Ptr(true), Fn: func(ctx context.Context) error { return nil }})
		h.waitStatus(key, "finished")
	}
	jobs := h.m.Jobs()
	if len(jobs) != 50 || jobs[0].Key != "h50" || jobs[49].Key != "h1" {
		t.Fatalf("history len=%d first=%s last=%s", len(jobs), jobs[0].Key, jobs[len(jobs)-1].Key)
	}
	var removed *apid.JobItem
	for _, e := range h.drainEvents() {
		var j apid.JobItem
		_ = json.Unmarshal(e.Data, &j)
		if j.Key == "h0" && e.Type == apid.EventTypeUpdate && j.IsRerunnable != nil && !*j.IsRerunnable {
			removed = &j
		}
	}
	if removed == nil || removed.Status != "finished" {
		t.Fatal("removed job should be announced as update with isRerunnable=false")
	}
}

func TestAddScheduleValidatesAndEmits(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 1, MaxStandby: 1})
	if err := h.m.AddSchedule(ScheduleItem{Key: "bad", Schedule: "* * *", Job: Item{Key: "j", Name: "J"}}); err == nil {
		t.Fatal("invalid schedule must error")
	}
	if err := h.m.AddSchedule(ScheduleItem{Key: "s", Schedule: "1 * * * *", Job: Item{Key: "j", Name: "J", Fn: func(ctx context.Context) error { return nil }}}); err != nil {
		t.Fatal(err)
	}
	if err := h.m.AddSchedule(ScheduleItem{Key: "s", Schedule: "bad", Job: Item{Key: "j2", Name: "J2"}}); err != nil {
		t.Fatal("duplicate schedule is ignored silently (warn)")
	}
	sch := h.m.Schedules()
	if len(sch) != 1 || sch[0].Key != "s" || sch[0].Schedule != "1 * * * *" || sch[0].Job.Key != "j" {
		t.Fatalf("schedules %+v", sch)
	}
	evs := h.drainEvents()
	if len(evs) != 1 || evs[0].Resource != apid.EventResourceJobSchedule || string(evs[0].Data) != `{"key":"s","schedule":"1 * * * *","job":{"key":"j","name":"J"}}` {
		t.Fatalf("events %+v", evs)
	}
}

func TestScheduleIsCheckedEveryMinuteFromStartup(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 1, MaxStandby: 1})
	var calls atomic.Int32
	_ = h.m.AddSchedule(ScheduleItem{Key: "s", Schedule: "2 * * * *", Job: Item{Key: "j", Name: "J", Fn: func(ctx context.Context) error { calls.Add(1); return nil }}})
	// 起動 00:00:30 → 00:01:30 (不一致) → 00:02:30 (一致)
	h.clock.Advance(time.Minute)
	if calls.Load() != 0 || len(h.m.Jobs()) != 0 {
		t.Fatal("should not run at minute 1")
	}
	h.clock.Advance(time.Minute)
	waitFor(t, func() bool { return calls.Load() == 1 })
	h.clock.Advance(59 * time.Minute) // 01:01:30
	if calls.Load() != 1 {
		t.Fatalf("calls %d", calls.Load())
	}
	h.clock.Advance(time.Minute) // 01:02:30
	waitFor(t, func() bool { return calls.Load() == 2 })
}

func TestScheduleThatFailsToMatchIsRemoved(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 1, MaxStandby: 1})
	// 正規表現は通るが範囲が逆
	if err := h.m.AddSchedule(ScheduleItem{Key: "rev", Schedule: "5-3 * * * *", Job: Item{Key: "j", Name: "J", Fn: func(ctx context.Context) error { return nil }}}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Minute)
	if len(h.m.Schedules()) != 0 {
		t.Fatal("invalid schedule should be removed after check")
	}
}

func TestRunSchedule(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 1, MaxStandby: 1})
	var calls atomic.Int32
	_ = h.m.AddSchedule(ScheduleItem{Key: "s", Schedule: "0 0 1 1 *", Job: Item{Key: "j", Name: "J", Fn: func(ctx context.Context) error { calls.Add(1); return nil }}})
	if !h.m.RunSchedule("s") || h.m.RunSchedule("nope") {
		t.Fatal("runSchedule result")
	}
	waitFor(t, func() bool { return calls.Load() == 1 })
}

func TestCloseAbortsRunningJobs(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 1, MaxStandby: 1})
	done := make(chan struct{})
	h.m.Add(Item{Key: "c", Name: "C", Fn: func(ctx context.Context) error { <-ctx.Done(); close(done); return ctx.Err() }})
	h.waitStatus("c", "running")
	h.m.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("close did not abort")
	}
}

func TestConcurrentAddIsSafe(t *testing.T) {
	h := newHarness(t, Options{MaxRunning: 4, MaxStandby: 8})
	var wg sync.WaitGroup
	var calls atomic.Int32
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h.m.Add(Item{Key: fmt.Sprintf("c%d", i), Name: "C", Fn: func(ctx context.Context) error { calls.Add(1); return nil }})
		}(i)
	}
	wg.Wait()
	waitFor(t, func() bool { h.clock.Advance(time.Second); return calls.Load() == 40 })
}
