package mlog

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

// 固定時刻を返すテスト用ロガーを作る。
func newTestLogger(t *testing.T, loc *time.Location) (*Logger, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errOut bytes.Buffer
	fixed := time.Date(2026, 10, 9, 7, 20, 1, 123_000_000, loc)
	l := New(Options{
		Stdout: &out,
		Stderr: &errOut,
		Now:    func() time.Time { return fixed },
	})
	return l, &out, &errOut
}

func TestLineFormatUsesLocalOffsetWithMilliseconds(t *testing.T) {
	jst := time.FixedZone("JST", 9*3600)
	l, out, _ := newTestLogger(t, jst)
	l.Info("hello %s", "world")
	want := "2026-10-09T07:20:01.123+09:00 info: hello world\n"
	if out.String() != want {
		t.Fatalf("got %q want %q", out.String(), want)
	}
}

func TestLineFormatNegativeOffsetFallsBackToUTCLikeNode(t *testing.T) {
	// Node 版は " GMT+dddd " にしかマッチしないので負オフセットでは toISOString (UTC, Z) になる
	est := time.FixedZone("EST", -5*3600)
	l, out, _ := newTestLogger(t, est)
	l.Info("x")
	want := "2026-10-09T12:20:01.123Z info: x\n"
	if out.String() != want {
		t.Fatalf("got %q want %q", out.String(), want)
	}
}

func TestLineFormatUTCZeroOffsetUsesPlusZero(t *testing.T) {
	l, out, _ := newTestLogger(t, time.UTC)
	l.Info("x")
	want := "2026-10-09T07:20:01.123+00:00 info: x\n"
	if out.String() != want {
		t.Fatalf("got %q want %q", out.String(), want)
	}
}

func TestMessageWithoutArgsIsVerbatim(t *testing.T) {
	l, out, _ := newTestLogger(t, time.UTC)
	l.Info("100% done")
	if !strings.HasSuffix(out.String(), "info: 100% done\n") {
		t.Fatalf("got %q", out.String())
	}
}

func TestStdoutStderrSplitLikeNodeConsole(t *testing.T) {
	l, out, errOut := newTestLogger(t, time.UTC)
	l.SetLevel(LevelDebug)
	l.Debug("d")
	l.Info("i")
	l.Warn("w")
	l.Error("e")
	l.Fatal("f")
	if got := out.String(); !strings.Contains(got, "debug: d") || !strings.Contains(got, "info: i") || strings.Contains(got, "warn") {
		t.Fatalf("stdout %q", got)
	}
	if got := errOut.String(); !strings.Contains(got, "warn: w") || !strings.Contains(got, "error: e") || !strings.Contains(got, "fatal: f") || strings.Contains(got, "info") {
		t.Fatalf("stderr %q", got)
	}
}

func TestLevelFilterSkipsOutputAndHistory(t *testing.T) {
	l, out, _ := newTestLogger(t, time.UTC)
	// 既定は INFO
	l.Debug("hidden")
	if out.Len() != 0 || len(l.History()) != 0 {
		t.Fatalf("debug should be filtered: %q %v", out.String(), l.History())
	}
	l.SetLevel(LevelError)
	l.Warn("hidden")
	l.Error("shown")
	if h := l.History(); len(h) != 1 || !strings.HasSuffix(h[0], "error: shown") {
		t.Fatalf("history %v", h)
	}
	l.SetLevel(LevelFatal)
	l.Error("hidden2")
	l.Fatal("f")
	if h := l.History(); len(h) != 2 {
		t.Fatalf("history %v", h)
	}
}

func TestHistoryIsBoundedByMaxLogHistory(t *testing.T) {
	l, _, _ := newTestLogger(t, time.UTC)
	if l.MaxHistory() != 1000 {
		t.Fatalf("default maxLogHistory = %d", l.MaxHistory())
	}
	l.SetMaxHistory(3)
	for i := 0; i < 5; i++ {
		l.Info("m%d", i)
	}
	h := l.History()
	if len(h) != 3 || !strings.HasSuffix(h[0], "m2") || !strings.HasSuffix(h[2], "m4") {
		t.Fatalf("history %v", h)
	}
}

func TestSubscribeReceivesLinesAndUnsubscribeStops(t *testing.T) {
	l, _, _ := newTestLogger(t, time.UTC)
	ch, cancel := l.Subscribe(4)
	l.Info("a")
	select {
	case got := <-ch:
		if !strings.HasSuffix(got, "info: a") {
			t.Fatalf("got %q", got)
		}
	default:
		t.Fatal("no line delivered")
	}
	cancel()
	cancel() // 二重解除しても panic しない
	l.Info("b")
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after unsubscribe")
	}
}

func TestSlowSubscriberDropsInsteadOfBlocking(t *testing.T) {
	l, _, _ := newTestLogger(t, time.UTC)
	_, cancel := l.Subscribe(1)
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			l.Info("x")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("logger blocked on slow subscriber")
	}
	if l.Dropped() != 9 {
		t.Fatalf("dropped = %d", l.Dropped())
	}
}

func TestWriteActsLikeMorganStream(t *testing.T) {
	l, out, _ := newTestLogger(t, time.UTC)
	n, err := l.Write([]byte("GET / 200\n"))
	if err != nil || n != len("GET / 200\n") {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if !strings.HasSuffix(out.String(), "info: GET / 200\n") {
		t.Fatalf("got %q", out.String())
	}
}

func TestConcurrentLoggingIsSafe(t *testing.T) {
	l, _, _ := newTestLogger(t, time.UTC)
	l.SetMaxHistory(50)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				l.Info("x")
				_ = l.History()
			}
		}()
	}
	wg.Wait()
	if len(l.History()) != 50 {
		t.Fatalf("history len %d", len(l.History()))
	}
}

func TestPackageLevelDefaultLogger(t *testing.T) {
	var out bytes.Buffer
	prev := SetDefault(New(Options{Stdout: &out, Stderr: &out}))
	defer SetDefault(prev)
	Info("pkg %d", 1)
	if !strings.Contains(out.String(), "info: pkg 1") {
		t.Fatalf("got %q", out.String())
	}
	if Default().MaxHistory() != 1000 {
		t.Fatal("default logger not set")
	}
}
