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

// Package mlog は Node 版 log.ts の移植。
// 行形式は `<ISO8601 ローカル時刻 (ms 付き, +hh:mm)> <level>: <message>` で、
// Node 版 getLogString と同一になるようにしている。
package mlog

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Level はログレベル (Node 版 LogLevel と同じ数値)。
type Level int

const (
	LevelFatal Level = -1
	LevelError Level = 0
	LevelWarn  Level = 1
	LevelInfo  Level = 2
	LevelDebug Level = 3
)

// DefaultMaxHistory は Node 版 maxLogHistory の既定値。
const DefaultMaxHistory = 1000

// Options は Logger の生成オプション。nil のフィールドは既定値を使う。
type Options struct {
	// Stdout は debug / info の出力先 (Node の console.log / console.info 相当)。
	Stdout io.Writer
	// Stderr は warn / error / fatal の出力先 (console.warn / console.error 相当)。
	Stderr io.Writer
	// Now は現在時刻 (テスト用に差し替え可能)。
	Now func() time.Time
}

// Logger はスレッドセーフなロガー。履歴と購読者を持つ。
//
// 購読者 (Subscribe) へはバッファ付きチャネルで非ブロッキング送信する。
// バッファが満杯の購読者への行は破棄し、Dropped() のカウンタを増やす
// (遅い /api/log/stream クライアントがログ出力全体を止めないため)。
type Logger struct {
	mu         sync.Mutex
	stdout     io.Writer
	stderr     io.Writer
	now        func() time.Time
	level      Level
	maxHistory int
	history    []string
	subs       map[int]chan string
	nextSubID  int
	dropped    atomic.Int64
}

// New は Logger を作る。既定レベルは INFO、履歴上限は 1000。
func New(opts Options) *Logger {
	l := &Logger{
		stdout:     opts.Stdout,
		stderr:     opts.Stderr,
		now:        opts.Now,
		level:      LevelInfo,
		maxHistory: DefaultMaxHistory,
		subs:       map[int]chan string{},
	}
	if l.stdout == nil {
		l.stdout = os.Stdout
	}
	if l.stderr == nil {
		l.stderr = os.Stderr
	}
	if l.now == nil {
		l.now = time.Now
	}
	return l
}

// SetLevel はログレベルを設定する。
func (l *Logger) SetLevel(level Level) {
	l.mu.Lock()
	l.level = level
	l.mu.Unlock()
}

// Level は現在のログレベルを返す。
func (l *Logger) Level() Level {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.level
}

// SetMaxHistory は履歴の上限を設定する。超過分は次回の出力時に古い順に捨てる。
func (l *Logger) SetMaxHistory(n int) {
	l.mu.Lock()
	l.maxHistory = n
	l.mu.Unlock()
}

// MaxHistory は履歴の上限を返す。
func (l *Logger) MaxHistory() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.maxHistory
}

// History は保持している履歴のコピーを返す (GET /api/log 用)。
func (l *Logger) History() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.history...)
}

// Dropped は購読者のバッファ溢れで破棄した行数の累計を返す。
func (l *Logger) Dropped() int64 { return l.dropped.Load() }

// Subscribe は新しいログ行を受け取るチャネルを返す。
// 返り値の cancel を呼ぶと購読を解除しチャネルを close する (複数回呼んでも安全)。
func (l *Logger) Subscribe(buffer int) (<-chan string, func()) {
	if buffer < 1 {
		buffer = 1
	}
	ch := make(chan string, buffer)
	l.mu.Lock()
	id := l.nextSubID
	l.nextSubID++
	l.subs[id] = ch
	l.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			l.mu.Lock()
			delete(l.subs, id)
			close(ch)
			l.mu.Unlock()
		})
	}
}

// Debug は debug レベルで出力する。
func (l *Logger) Debug(format string, args ...any) { l.log(LevelDebug, "debug", format, args) }

// Info は info レベルで出力する。
func (l *Logger) Info(format string, args ...any) { l.log(LevelInfo, "info", format, args) }

// Warn は warn レベルで出力する。
func (l *Logger) Warn(format string, args ...any) { l.log(LevelWarn, "warn", format, args) }

// Error は error レベルで出力する。
func (l *Logger) Error(format string, args ...any) { l.log(LevelError, "error", format, args) }

// Fatal は fatal レベルで出力する (プロセスは終了しない。Node 版と同じ)。
func (l *Logger) Fatal(format string, args ...any) { l.log(LevelFatal, "fatal", format, args) }

// Write は io.Writer 実装。Node 版 LogEvent.write (morgan のストリーム) と同じく
// 末尾 1 文字 (改行) を落として info で出力する。
func (l *Logger) Write(p []byte) (int, error) {
	s := string(p)
	if len(s) > 0 {
		s = s[:len(s)-1]
	}
	l.log(LevelInfo, "info", "%s", []any{s})
	return len(p), nil
}

func (l *Logger) log(level Level, lvstr, format string, args []any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Node 版: logLevel < level なら何もしない (履歴にも残さない)
	if l.level < level {
		return
	}

	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	line := FormatTime(l.now()) + " " + lvstr + ": " + msg

	l.history = append(l.history, line)
	if over := len(l.history) - l.maxHistory; over > 0 {
		// 古い行を捨てる (スライスの先頭を詰め直してメモリを解放する)
		l.history = append([]string(nil), l.history[over:]...)
	}

	w := l.stdout
	if level <= LevelWarn {
		w = l.stderr
	}
	_, _ = io.WriteString(w, line+"\n")

	for _, ch := range l.subs {
		select {
		case ch <- line:
		default:
			l.dropped.Add(1)
		}
	}
}

// FormatTime は Node 版 getLogString のタイムスタンプ部分を再現する。
// Node 版は Date#toString が " GMT+hhmm " (0 以上のオフセット) のときだけ
// ローカル時刻 + "+hh:mm" を出力し、負のオフセットでは toISOString (UTC, "Z") になる。
func FormatTime(t time.Time) string {
	_, offset := t.Zone()
	if offset >= 0 {
		return t.Format("2006-01-02T15:04:05.000") + fmt.Sprintf("+%02d:%02d", offset/3600, (offset%3600)/60)
	}
	return t.UTC().Format("2006-01-02T15:04:05.000") + "Z"
}

var (
	defaultMu     sync.RWMutex
	defaultLogger = New(Options{})
)

// Default はパッケージ既定のロガーを返す。
func Default() *Logger {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultLogger
}

// SetDefault は既定のロガーを差し替え、以前のロガーを返す。
func SetDefault(l *Logger) *Logger {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	prev := defaultLogger
	defaultLogger = l
	return prev
}

// Debug は既定ロガーへ debug で出力する。
func Debug(format string, args ...any) { Default().Debug(format, args...) }

// Info は既定ロガーへ info で出力する。
func Info(format string, args ...any) { Default().Info(format, args...) }

// Warn は既定ロガーへ warn で出力する。
func Warn(format string, args ...any) { Default().Warn(format, args...) }

// Error は既定ロガーへ error で出力する。
func Error(format string, args ...any) { Default().Error(format, args...) }

// Fatal は既定ロガーへ fatal で出力する。
func Fatal(format string, args ...any) { Default().Fatal(format, args...) }
