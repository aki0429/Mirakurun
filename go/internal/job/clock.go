package job

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Clock は時刻とタイマーの抽象。テストでは FakeClock を注入して決定的に動かす。
// core パッケージ (Program / Service / Channel) も同じ Clock を共有する。
type Clock interface {
	// Now は現在時刻を返す。
	Now() time.Time
	// AfterFunc は d 経過後に f を (別 goroutine または Advance 呼び出し元で) 実行する。
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer は AfterFunc が返すタイマー。
type Timer interface {
	// Stop は未発火のタイマーを止め、止めた場合 true を返す。
	Stop() bool
}

// RealClock は実時間の Clock。
type RealClock struct{}

// Now は time.Now を返す。
func (RealClock) Now() time.Time { return time.Now() }

// AfterFunc は time.AfterFunc を使う。
func (RealClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// Sleep は Clock 上で d 待つ。ctx がキャンセルされたら ctx.Err() を返す。
func Sleep(ctx context.Context, c Clock, d time.Duration) error {
	ch := make(chan struct{})
	t := c.AfterFunc(d, func() { close(ch) })
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		t.Stop()
		return ctx.Err()
	}
}

// FakeClock は手動で進めるテスト用 Clock。
// Advance 中に期限を迎えたタイマーのコールバックは Advance の呼び出し goroutine で
// 期限順に同期実行される (コールバック内で追加されたタイマーも同じ Advance 内で発火しうる)。
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	seq    int
	timers []*fakeTimer
}

type fakeTimer struct {
	c        *FakeClock
	deadline time.Time
	seq      int
	f        func()
	stopped  bool
}

// NewFakeClock は start から始まる FakeClock を作る。
func NewFakeClock(start time.Time) *FakeClock { return &FakeClock{now: start} }

// Now は現在の仮想時刻を返す。
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// AfterFunc は仮想時刻上のタイマーを登録する。
func (c *FakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	t := &fakeTimer{c: c, deadline: c.now.Add(d), seq: c.seq, f: f}
	c.timers = append(c.timers, t)
	return t
}

// Stop はタイマーを止める。
func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	for i, x := range t.c.timers {
		if x == t {
			t.c.timers = append(t.c.timers[:i], t.c.timers[i+1:]...)
			t.stopped = true
			return true
		}
	}
	return false
}

// PendingTimers は未発火のタイマー数を返す。
func (c *FakeClock) PendingTimers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

// Advance は仮想時刻を d 進め、期限を迎えたタイマーを順に発火する。
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		sort.SliceStable(c.timers, func(i, j int) bool {
			if c.timers[i].deadline.Equal(c.timers[j].deadline) {
				return c.timers[i].seq < c.timers[j].seq
			}
			return c.timers[i].deadline.Before(c.timers[j].deadline)
		})
		if len(c.timers) == 0 || c.timers[0].deadline.After(target) {
			c.now = target
			c.mu.Unlock()
			return
		}
		t := c.timers[0]
		c.timers = c.timers[1:]
		if t.deadline.After(c.now) {
			c.now = t.deadline
		}
		c.mu.Unlock()
		t.f()
	}
}
