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

package tuner

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/core"
)

// deviceUser はチューナー利用者 1 人分の内部状態 (Node 版 TunerDevice の User)。
type deviceUser struct {
	req     UserRequest
	setting *apid.StreamSetting
	stream  *TSFilter
}

// TunerDevice は 1 つのチューナーデバイス (Node 版 TunerDevice.ts の移植)。
// tuners.yml の command を子プロセスとして起動し、stdout の TS を利用者へ中継する。
type TunerDevice struct {
	// opMu は startStream / kill / shutdown を直列化する (Node の単一イベントループに相当)。
	opMu sync.Mutex
	// mu は状態を保護する。
	mu sync.Mutex

	app    *core.App
	index  int
	config apid.ConfigTunersItem

	channel    *core.ChannelItem
	command    string
	process    *exec.Cmd
	catProcess *exec.Cmd
	users      map[*deviceUser]bool

	isAvailable bool
	isRemote    bool
	isFault     bool
	fatalCount  int
	exited      bool
	closing     bool

	releaseCh chan struct{}
}

// newTunerDevice は TunerDevice を生成する (子プロセスは最初のストリーム要求まで起動しない)。
func newTunerDevice(app *core.App, index int, config apid.ConfigTunersItem) *TunerDevice {
	d := &TunerDevice{
		app:         app,
		index:       index,
		config:      config,
		users:       map[*deviceUser]bool{},
		isAvailable: true,
		isRemote:    config.RemoteMirakurunHost != "",
	}
	d.app.Log.Debug("TunerDevice#%d initialized", index)
	if app.Event != nil {
		app.Event.Emit(apid.EventResourceTuner, apid.EventTypeCreate, d.Status())
	}
	return d
}

// Index はデバイス番号を返す。
func (d *TunerDevice) Index() int { return d.index }

// Name はデバイス名を返す。
func (d *TunerDevice) Name() string { return d.config.Name }

// Types は対応するチャンネル種別を返す。
func (d *TunerDevice) Types() []apid.ChannelType { return d.config.Types }

// Config は tuners.yml の設定を返す。
func (d *TunerDevice) Config() apid.ConfigTunersItem { return d.config }

// IsFree は空きデバイスか返す。
func (d *TunerDevice) IsFree() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.isAvailable && d.channel == nil && len(d.users) == 0
}

// IsAvailable は利用可能か返す。
func (d *TunerDevice) IsAvailable() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.isAvailable
}

// IsFault は故障状態か返す。
func (d *TunerDevice) IsFault() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.isFault
}

// IsUsing は使用中か返す。
func (d *TunerDevice) IsUsing() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.isAvailable && d.channel != nil && len(d.users) != 0
}

// IsRemote はリモートデバイスか返す。
func (d *TunerDevice) IsRemote() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.isRemote
}

// Channel は現在のチャンネルを返す (未使用時 nil)。
func (d *TunerDevice) Channel() *core.ChannelItem {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.channel
}

// decoderCommand は復号コマンドを返す (不要なら "")。
func (d *TunerDevice) decoderCommand() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.isRemote && d.config.RemoteMirakurunDecoder != nil && *d.config.RemoteMirakurunDecoder {
		return ""
	}
	return d.config.Decoder
}

// GetPriority は利用者の最大 priority を返す (利用者なしは -2)。
func (d *TunerDevice) GetPriority() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.priorityLocked()
}

func (d *TunerDevice) priorityLocked() int {
	priority := -2
	for user := range d.users {
		if user.req.Priority > priority {
			priority = user.req.Priority
		}
	}
	return priority
}

// Users は利用者一覧を返す。
func (d *TunerDevice) Users() []apid.TunerUser {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]apid.TunerUser, 0, len(d.users))
	for user := range d.users {
		info := apid.StreamInfo{}
		if user.stream != nil {
			info = user.stream.StreamInfo()
		}
		out = append(out, apid.TunerUser{
			ID:             user.req.ID,
			Priority:       user.req.Priority,
			Agent:          user.req.Agent,
			URL:            user.req.URL,
			DisableDecoder: user.req.DisableDecoder,
			StreamSetting:  user.setting,
			StreamInfo:     info,
		})
	}
	return out
}

// Status は /api/tuners の 1 要素を返す (Node 版 toJSON)。
func (d *TunerDevice) Status() apid.TunerDevice {
	d.mu.Lock()
	defer d.mu.Unlock()

	var command *string
	if d.command != "" {
		command = apid.Ptr(d.command)
	}
	var pid *int
	if d.process != nil && d.process.Process != nil {
		pid = apid.Ptr(d.process.Process.Pid)
	}

	users := make([]apid.TunerUser, 0, len(d.users))
	for user := range d.users {
		info := apid.StreamInfo{}
		if user.stream != nil {
			info = user.stream.StreamInfo()
		}
		users = append(users, apid.TunerUser{
			ID:             user.req.ID,
			Priority:       user.req.Priority,
			Agent:          user.req.Agent,
			URL:            user.req.URL,
			DisableDecoder: user.req.DisableDecoder,
			StreamSetting:  user.setting,
			StreamInfo:     info,
		})
	}

	return apid.TunerDevice{
		Index:       d.index,
		Name:        d.config.Name,
		Types:       d.config.Types,
		Command:     command,
		PID:         pid,
		Users:       users,
		IsAvailable: d.isAvailable,
		IsRemote:    d.isRemote,
		IsFree:      d.isAvailable && d.channel == nil && len(d.users) == 0,
		IsUsing:     d.isAvailable && d.channel != nil && len(d.users) != 0,
		IsFault:     d.isFault,
	}
}

// startStream は利用者をデバイスへ接続する (Node 版 startStream)。
func (d *TunerDevice) startStream(user *deviceUser, stream *TSFilter, channel *core.ChannelItem) error {
	d.opMu.Lock()
	defer d.opMu.Unlock()

	d.mu.Lock()
	if !d.isAvailable {
		d.mu.Unlock()
		return fmt.Errorf("TunerDevice#%d is not available", d.index)
	}
	if channel == nil && d.process == nil {
		d.mu.Unlock()
		return fmt.Errorf("TunerDevice#%d has not stream", d.index)
	}

	needSpawn := false
	if channel != nil {
		if !typeIncluded(d.config.Types, channel.Type) {
			d.mu.Unlock()
			return fmt.Errorf("TunerDevice#%d is not supported for channel type `%s`", d.index, channel.Type)
		}
		if d.process != nil {
			if d.channel == nil || channel.Channel != d.channel.Channel {
				if user.req.Priority <= d.priorityLocked() {
					d.mu.Unlock()
					return fmt.Errorf("TunerDevice#%d has higher priority user", d.index)
				}
				d.mu.Unlock()
				d.killWait(true)
				d.mu.Lock()
				needSpawn = true
			}
		} else {
			needSpawn = true
		}
	}
	d.mu.Unlock()

	if needSpawn {
		if err := d.spawn(channel); err != nil {
			return err
		}
	}

	d.mu.Lock()
	user.stream = stream
	d.users[user] = true
	d.mu.Unlock()

	d.info("TunerDevice#%d streaming to user `%s` (priority=%d)", d.index, user.req.ID, user.req.Priority)

	stream.OnClose(func() { d.endStream(user) })
	d.updated()
	return nil
}

// endStream は利用者を切断する (Node 版 endStream)。
func (d *TunerDevice) endStream(user *deviceUser) {
	d.mu.Lock()
	if !d.users[user] {
		d.mu.Unlock()
		return
	}
	delete(d.users, user)
	empty := len(d.users) == 0
	d.mu.Unlock()

	if user.stream != nil {
		_ = user.stream.Close()
	}

	d.debug("TunerDevice#%d end stream for user `%s` (priority=%d)", d.index, user.req.ID, user.req.Priority)
	d.updated()

	if empty {
		time.AfterFunc(3*time.Second, func() {
			d.mu.Lock()
			empty := len(d.users) == 0
			hasProcess := d.process != nil
			d.mu.Unlock()
			if empty && hasProcess {
				d.killWait(true)
			}
		})
	}
}

// spawn は子プロセスを起動する (Node 版 _spawn)。
func (d *TunerDevice) spawn(ch *core.ChannelItem) error {
	if d.app == nil {
		return errors.New("tuner device has no app")
	}

	d.mu.Lock()
	if d.process != nil {
		d.mu.Unlock()
		return fmt.Errorf("TunerDevice#%d has process", d.index)
	}
	if d.isRemote {
		d.mu.Unlock()
		return fmt.Errorf("TunerDevice#%d is a remote device (not supported)", d.index)
	}
	d.mu.Unlock()

	cmdline := ReplaceCommandTemplate(d.config.Command, buildCommandVars(ch))
	parsedCommand, args, err := ParseCommandForSpawn(cmdline)
	if err != nil {
		return fmt.Errorf("TunerDevice#%d command parse failed: %w", d.index, err)
	}

	cmd := exec.Command(parsedCommand, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("TunerDevice#%d stdout pipe: %w", d.index, err)
	}
	cmd.Stderr = &deviceLogWriter{device: d}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("TunerDevice#%d start: %w", d.index, err)
	}

	d.mu.Lock()
	d.process = cmd
	d.command = cmdline
	d.channel = ch
	d.exited = false
	d.releaseCh = make(chan struct{})
	d.mu.Unlock()

	reader := io.ReadCloser(stdout)
	if d.config.DVBDevicePath != "" {
		cat := exec.Command("cat", d.config.DVBDevicePath)
		catOut, cerr := cat.StdoutPipe()
		if cerr == nil {
			if serr := cat.Start(); serr == nil {
				d.mu.Lock()
				d.catProcess = cat
				d.mu.Unlock()
				reader = catOut
				go func() {
					_ = cat.Wait()
					d.mu.Lock()
					exited := d.exited
					d.mu.Unlock()
					if !exited {
						d.killWait(false)
					}
				}()
			}
		}
	}

	go d.readLoop(reader)
	go d.waitLoop(cmd)

	d.updated()
	d.info("TunerDevice#%d process has spawned by command `%s` (pid=%d)", d.index, cmdline, cmd.Process.Pid)
	return nil
}

// readLoop は子プロセスの stdout を利用者全員へ中継する (Node 版 _streamOnData)。
func (d *TunerDevice) readLoop(reader io.ReadCloser) {
	buf := make([]byte, 32*1024)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			d.mu.Lock()
			users := make([]*deviceUser, 0, len(d.users))
			for u := range d.users {
				users = append(users, u)
			}
			d.mu.Unlock()
			for _, u := range users {
				if u.stream != nil {
					_, _ = u.stream.Write(chunk)
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// waitLoop は子プロセスの終了を待って _end 相当の処理を行う。
func (d *TunerDevice) waitLoop(cmd *exec.Cmd) {
	_ = cmd.Wait()

	d.mu.Lock()
	if d.process != cmd {
		d.mu.Unlock()
		return
	}
	d.exited = true
	d.isAvailable = false
	var toClose []*deviceUser
	if d.closing {
		for u := range d.users {
			toClose = append(toClose, u)
		}
		d.users = map[*deviceUser]bool{}
	}
	cat := d.catProcess
	d.catProcess = nil
	delay := 100 * time.Millisecond
	if d.config.DVBDevicePath != "" {
		delay = time.Second
	}
	d.mu.Unlock()

	if cat != nil && cat.Process != nil {
		_ = cat.Process.Kill()
	}
	for _, u := range toClose {
		if u.stream != nil {
			_ = u.stream.Close()
		}
	}
	d.updated()
	time.AfterFunc(delay, func() { d.release(cmd) })
}

// release は子プロセス終了後の後始末を行う (Node 版 _release)。
func (d *TunerDevice) release(cmd *exec.Cmd) {
	d.mu.Lock()
	if d.process != cmd {
		d.mu.Unlock()
		return
	}
	d.process = nil
	d.command = ""
	d.mu.Unlock()

	d.mu.Lock()
	if !d.closing && len(d.users) != 0 {
		if d.app != nil && d.app.Status != nil {
			d.app.Status.AddError(core.ErrorTunerDeviceRespawn)
		}
		d.logWarn("TunerDevice#%d respawning because request has not closed", d.index)
		ch := d.channel
		d.mu.Unlock()
		_ = d.spawn(ch)
		return
	}
	d.fatalCount = 0
	d.channel = nil
	d.users = map[*deviceUser]bool{}
	if !d.isFault {
		d.isAvailable = true
	}
	d.closing = false
	d.exited = false
	rc := d.releaseCh
	d.releaseCh = nil
	d.mu.Unlock()

	if rc != nil {
		close(rc)
	}
	d.info("TunerDevice#%d released", d.index)
	d.updated()
}

// killWait は子プロセスを停止して解放を待つ (Node 版 _kill)。
func (d *TunerDevice) killWait(close bool) {
	d.mu.Lock()
	if d.process == nil || d.process.Process == nil {
		d.mu.Unlock()
		return
	}
	if d.closing {
		d.mu.Unlock()
		return
	}
	d.isAvailable = false
	d.closing = close
	proc := d.process
	rc := d.releaseCh
	d.mu.Unlock()

	d.updated()

	// Windows では SIGTERM が無いため即時 Kill する (Node の SIGTERM→6 秒後 SIGKILL 相当)。
	if proc.Process != nil {
		_ = proc.Process.Kill()
	}
	if rc != nil {
		select {
		case <-rc:
		case <-time.After(6 * time.Second):
		}
	}
}

// KillProcess はデバイスの子プロセスを停止する (/api/tuners/{index}/process)。
func (d *TunerDevice) KillProcess() {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	d.killWait(true)
}

// shutdown はデバイスを確実に停止する (プロセス停止 + 利用者ストリームのクローズ)。
func (d *TunerDevice) shutdown() {
	d.opMu.Lock()
	defer d.opMu.Unlock()

	d.mu.Lock()
	d.closing = true
	d.isAvailable = false
	users := make([]*deviceUser, 0, len(d.users))
	for u := range d.users {
		users = append(users, u)
	}
	d.users = map[*deviceUser]bool{}
	proc := d.process
	rc := d.releaseCh
	d.mu.Unlock()

	for _, u := range users {
		if u.stream != nil {
			_ = u.stream.Close()
		}
	}
	if proc != nil && proc.Process != nil {
		_ = proc.Process.Kill()
	}
	if rc != nil {
		select {
		case <-rc:
		case <-time.After(3 * time.Second):
		}
	}
}

func (d *TunerDevice) updated() {
	if d.app != nil && d.app.Event != nil {
		d.app.Event.Emit(apid.EventResourceTuner, apid.EventTypeUpdate, d.Status())
	}
}

func (d *TunerDevice) info(format string, args ...any) {
	if d.app != nil && d.app.Log != nil {
		d.app.Log.Info(format, args...)
	}
}
func (d *TunerDevice) debug(format string, args ...any) {
	if d.app != nil && d.app.Log != nil {
		d.app.Log.Debug(format, args...)
	}
}
func (d *TunerDevice) warn(format string, args ...any) {
	if d.app != nil && d.app.Log != nil {
		d.app.Log.Warn(format, args...)
	}
}
func (d *TunerDevice) logWarn(format string, args ...any) { d.warn(format, args...) }

// deviceLogWriter は子プロセスの stderr を行単位で debug ログへ流す。
type deviceLogWriter struct {
	device *TunerDevice
	buf    []byte
}

func (w *deviceLogWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := indexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		w.device.debug("TunerDevice#%d > %s", w.device.index, trimSpace(line))
	}
	return len(p), nil
}

// buildCommandVars は replacementCommandTemplate へ渡す変数を組み立てる (Node 版 _spawn)。
func buildCommandVars(ch *core.ChannelItem) map[string]any {
	vars := map[string]any{
		"channel": ch.Channel,
		"type":    string(ch.Type),
	}
	sat := ""
	if ch.CommandVars != nil {
		if v, ok := ch.CommandVars["satellite"]; ok {
			sat = stringifyNode(v)
		} else if v, ok := ch.CommandVars["satelite"]; ok {
			sat = stringifyNode(v)
		}
	}
	vars["satelite"] = sat // deprecated, for backward compatibility
	vars["space"] = 0      // default value for backward compatibility
	for k, v := range ch.CommandVars {
		vars[k] = v
	}
	return vars
}

func typeIncluded(types []apid.ChannelType, t apid.ChannelType) bool {
	for _, x := range types {
		if x == t {
			return true
		}
	}
	return false
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t' || s[start] == '\r') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
