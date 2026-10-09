package job

import (
	"context"
	"testing"
	"time"
)

func TestFakeClockFiresTimersInDeadlineOrder(t *testing.T) {
	c := NewFakeClock(time.UnixMilli(1000))
	var order []int
	c.AfterFunc(3*time.Second, func() { order = append(order, 3) })
	c.AfterFunc(1*time.Second, func() { order = append(order, 1) })
	stopped := c.AfterFunc(2*time.Second, func() { order = append(order, 2) })
	if !stopped.Stop() {
		t.Fatal("Stop should report true for pending timer")
	}
	if c.PendingTimers() != 2 {
		t.Fatalf("pending %d", c.PendingTimers())
	}
	c.Advance(1500 * time.Millisecond)
	if len(order) != 1 || order[0] != 1 {
		t.Fatalf("order %v", order)
	}
	c.Advance(10 * time.Second)
	if len(order) != 2 || order[1] != 3 {
		t.Fatalf("order %v", order)
	}
	if got := c.Now().UnixMilli(); got != 1000+11500 {
		t.Fatalf("now %d", got)
	}
}

func TestFakeClockTimerScheduledFromCallbackFiresWithinSameAdvance(t *testing.T) {
	c := NewFakeClock(time.UnixMilli(0))
	n := 0
	var tick func()
	tick = func() {
		n++
		c.AfterFunc(time.Second, tick)
	}
	c.AfterFunc(time.Second, tick)
	c.Advance(5 * time.Second)
	if n != 5 {
		t.Fatalf("n=%d", n)
	}
}

func TestSleepReturnsOnAdvanceOrContextCancel(t *testing.T) {
	c := NewFakeClock(time.UnixMilli(0))
	done := make(chan error, 1)
	go func() { done <- Sleep(context.Background(), c, time.Minute) }()
	waitFor(t, func() bool { return c.PendingTimers() == 1 })
	c.Advance(time.Minute)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- Sleep(ctx, c, time.Minute) }()
	waitFor(t, func() bool { return c.PendingTimers() == 1 })
	cancel()
	if err := <-done; err == nil {
		t.Fatal("expected ctx error")
	}
	if c.PendingTimers() != 0 {
		t.Fatal("timer should be stopped after cancel")
	}
}

func TestRealClockAfterFunc(t *testing.T) {
	c := RealClock{}
	ch := make(chan struct{})
	c.AfterFunc(time.Millisecond, func() { close(ch) })
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("real timer did not fire")
	}
}

// waitFor は条件が真になるまで (最大 5 秒) 待つテストヘルパ。
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(time.Millisecond)
	}
}
