/*
   Copyright 2021 kanreisa
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

package tuner

import (
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/core"
)

// TSDecoder は外部デコーダ (ffmpeg など) を子プロセスとして起動し、TS を stdin へ書き込み、
// stdout の出力 (MP4 など) を次の io.Writer へ流す (Node 版 TSDecoder.ts の移植)。
type TSDecoder struct {
	mu      sync.Mutex
	app     *core.App
	id      int
	command string
	output  io.Writer

	process    *exec.Cmd
	processPID int
	stdin      io.WriteCloser
	closed     bool
	isNew      bool
	deadCount  int
	fallback   bool
	timeout    *time.Timer

	onClose []func()
}

var decoderIDCounter int
var decoderIDMu sync.Mutex

// newTSDecoder は TSDecoder を生成し、復号子プロセスを起動する。
func newTSDecoder(app *core.App, output io.Writer, command string) *TSDecoder {
	decoderIDMu.Lock()
	id := decoderIDCounter
	decoderIDCounter++
	decoderIDMu.Unlock()

	d := &TSDecoder{app: app, id: id, command: command, output: output}
	if app != nil && app.Status != nil {
		app.Status.AddDecoder(1)
	}
	d.logInfo("TSDecoder#%d has created (command=%s)", d.id, d.command)
	d.spawn()
	return d
}

// Write はデコーダの stdin へ TS データを書き込む。
func (d *TSDecoder) Write(p []byte) (int, error) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return len(p), nil
	}
	if d.fallback {
		out := d.output
		d.mu.Unlock()
		if out == nil {
			return len(p), nil
		}
		return out.Write(p)
	}
	stdin := d.stdin
	if stdin == nil {
		d.mu.Unlock()
		return len(p), nil
	}
	if d.isNew && d.process != nil {
		d.isNew = false
		proc := d.process
		d.timeout = time.AfterFunc(1500*time.Millisecond, func() {
			d.logWarn("TSDecoder#%d process will force killed because no respond...", d.id)
			if proc.Process != nil {
				_ = proc.Process.Kill()
			}
		})
	}
	d.mu.Unlock()

	return stdin.Write(p)
}

// Close は TSDecoder を閉じる (Node 版 _close)。
func (d *TSDecoder) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	if d.timeout != nil {
		d.timeout.Stop()
	}
	d.killLocked()
	out := d.output
	d.output = nil
	cb := d.onClose
	d.onClose = nil
	if d.app != nil && d.app.Status != nil {
		d.app.Status.AddDecoder(-1)
	}
	d.logInfo("TSDecoder#%d has closed (command=%s)", d.id, d.command)
	d.mu.Unlock()

	if out != nil {
		if c, ok := out.(io.Closer); ok {
			_ = c.Close()
		}
	}
	for _, fn := range cb {
		fn()
	}
	return nil
}

func (d *TSDecoder) spawn() {
	d.mu.Lock()
	if d.closed || d.process != nil {
		d.mu.Unlock()
		return
	}
	if d.deadCount > 0 {
		if d.app != nil && d.app.Status != nil {
			d.app.Status.AddError(core.ErrorDecoderRespawn)
		}
		d.logWarn("TSDecoder#%d respawning because dead (count=%d)", d.id, d.deadCount)
	}
	command := d.command
	d.mu.Unlock()

	parsedCommand, args, err := ParseCommandForSpawn(command)
	if err != nil {
		d.logError("TSDecoder#%d command parse failed `%s`", d.id, command)
		d.fallbackOn()
		return
	}

	cmd := exec.Command(parsedCommand, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		d.logError("TSDecoder#%d stdin pipe failed", d.id)
		d.fallbackOn()
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		d.logError("TSDecoder#%d stdout pipe failed", d.id)
		d.fallbackOn()
		return
	}
	if err := cmd.Start(); err != nil {
		d.logError("TSDecoder#%d process start failed: %s", d.id, err)
		d.fallbackOn()
		return
	}

	d.mu.Lock()
	d.process = cmd
	d.processPID = cmd.Process.Pid
	d.stdin = stdin
	d.isNew = true
	d.mu.Unlock()

	d.logInfo("TSDecoder#%d process has spawned by command `%s` (pid=%d)", d.id, command, cmd.Process.Pid)

	go d.pump(stdout)
	go d.wait(cmd)
}

// pump はデコーダ stdout を次の Writer へ流す。
func (d *TSDecoder) pump(stdout io.Reader) {
	first := true
	buf := make([]byte, 32*1024)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			if first {
				first = false
				d.mu.Lock()
				if d.timeout != nil {
					d.timeout.Stop()
					d.timeout = nil
				}
				out := d.output
				d.mu.Unlock()
				if out != nil {
					_, _ = out.Write(buf[:n])
				}
			} else {
				d.mu.Lock()
				out := d.output
				d.mu.Unlock()
				if out != nil {
					_, _ = out.Write(buf[:n])
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// wait は子プロセスの終了を待って _dead 相当の処理を行う。
func (d *TSDecoder) wait(cmd *exec.Cmd) {
	_ = cmd.Wait()
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	d.dead(cmd)
}

func (d *TSDecoder) dead(cmd *exec.Cmd) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.logError("TSDecoder#%d unexpected dead", d.id)
	d.deadCount++
	d.killLocked()
	fallback := d.deadCount > 3
	d.mu.Unlock()

	if fallback {
		d.fallbackOn()
		return
	}
	time.AfterFunc(1500*time.Millisecond, d.spawn)
}

// fallbackOn はパススルーへ切り替える (Node 版 _fallback)。
func (d *TSDecoder) fallbackOn() {
	d.mu.Lock()
	if d.closed || d.fallback {
		d.mu.Unlock()
		return
	}
	d.fallback = true
	d.mu.Unlock()
	d.logWarn("TSDecoder#%d has been fallback into pass-through stream", d.id)
}

func (d *TSDecoder) killLocked() {
	if d.process != nil {
		if d.process.Process != nil {
			_ = d.process.Process.Kill()
		}
		d.process = nil
		d.processPID = 0
	}
	d.stdin = nil
}

func (d *TSDecoder) logInfo(format string, args ...any) {
	if d.app != nil && d.app.Log != nil {
		d.app.Log.Info(format, args...)
	}
}
func (d *TSDecoder) logWarn(format string, args ...any) {
	if d.app != nil && d.app.Log != nil {
		d.app.Log.Warn(format, args...)
	}
}
func (d *TSDecoder) logError(format string, args ...any) {
	if d.app != nil && d.app.Log != nil {
		d.app.Log.Error(format, args...)
	}
}
