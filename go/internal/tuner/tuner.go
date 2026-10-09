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
	"context"
	"errors"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/core"
)

// UserRequest はストリーム開始要求 1 件 (Node 版 common.UserRequest)。
type UserRequest struct {
	ID             string
	Priority       int
	Agent          string
	URL            string
	DisableDecoder bool
}

// Tuner はチューナーデバイス群を管理する (Node 版 Tuner.ts の移植)。
type Tuner struct {
	mu      sync.Mutex
	app     *core.App
	devices []*TunerDevice
	// picked は readyForJob で一時的に確保したデバイス (Node 版 _readyForJobPickedDeviceSet)。
	picked map[*TunerDevice]bool
}

// New は tuners.yml からチューナーデバイスを生成する。
// Node と同様に子プロセスはストリーム要求時に遅延起動する。
func New(app *core.App) *Tuner {
	t := &Tuner{app: app, picked: map[*TunerDevice]bool{}}
	t.load()
	if app != nil && app.Status != nil {
		app.Status.TunerDeviceCount = t.DeviceCount
	}
	return t
}

// Start はチューナーの利用を開始する。デバイスは New で生成済みで、子プロセスは遅延起動するため
// ここでは何もしない (冪等)。
func (t *Tuner) Start(ctx context.Context) error {
	if t.app != nil && t.app.Log != nil {
		t.app.Log.Info("%d tuners loaded", t.DeviceCount())
	}
	return nil
}

// Shutdown は全デバイス・全ストリームを停止する (Node の killAllTuners/killAllDecoder 相当)。
func (t *Tuner) Shutdown() {
	t.mu.Lock()
	devices := make([]*TunerDevice, len(t.devices))
	copy(devices, t.devices)
	t.mu.Unlock()
	for _, d := range devices {
		d.shutdown()
	}
}

// load は tuners.yml を検証しながらデバイスを生成する (Node 版 _load)。
func (t *Tuner) load() {
	if t.app == nil || t.app.Config == nil {
		return
	}
	tuners := t.app.Config.Tuners
	for i, tuner := range tuners {
		if tuner.Name == "" || len(tuner.Types) == 0 || (tuner.RemoteMirakurunHost == "" && tuner.Command == "") {
			t.app.Log.Error("missing required property in tuner#%d configuration", i)
			continue
		}
		if tuner.IsDisabled {
			continue
		}
		t.devices = append(t.devices, newTunerDevice(t.app, i, tuner))
	}
	t.app.Log.Info("%d of %d tuners loaded", len(t.devices), len(tuners))
}

// Devices は /api/tuners の JSON 配列を返す。
func (t *Tuner) Devices() []apid.TunerDevice {
	t.mu.Lock()
	devices := make([]*TunerDevice, len(t.devices))
	copy(devices, t.devices)
	t.mu.Unlock()

	out := make([]apid.TunerDevice, 0, len(devices))
	for _, d := range devices {
		out = append(out, d.Status())
	}
	return out
}

// Get は index 一致のデバイスを返す (無ければ nil)。
func (t *Tuner) Get(index int) *TunerDevice {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, d := range t.devices {
		if d.index == index {
			return d
		}
	}
	return nil
}

// DeviceCount はデバイス数を返す。
func (t *Tuner) DeviceCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.devices)
}

// TypeExists は指定の種別を扱えるデバイスが存在するか返す。
func (t *Tuner) TypeExists(typ apid.ChannelType) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, d := range t.devices {
		if typeIncluded(d.config.Types, typ) {
			return true
		}
	}
	return false
}

// ReadyForJob はチャンネルがジョブ実行可能か返す (Node 版 readyForJob)。
func (t *Tuner) ReadyForJob(ctx context.Context, ch *core.ChannelItem) bool {
	devices := t.devicesByType(ch.Type)
	if len(devices) == 0 {
		t.app.Log.Error("readyForJob: no tuners for channel type: %s", ch.Type)
		return false
	}

	for {
		t.mu.Lock()
		pickable := make([]*TunerDevice, 0, len(devices))
		for _, d := range devices {
			if !t.picked[d] {
				pickable = append(pickable, d)
			}
		}
		t.mu.Unlock()

		if len(pickable) == 0 {
			t.app.Log.Debug("readyForJob: no pickable tuners for channel type: %s", ch.Type)
			if !sleepCtx(ctx, 10*time.Second) {
				return false
			}
			continue
		}
		device := t.pickTunerDevice(pickable, ch, -1)
		if device == nil {
			if !sleepCtx(ctx, 10*time.Second) {
				return false
			}
			continue
		}

		t.mu.Lock()
		t.picked[device] = true
		t.mu.Unlock()
		t.app.Log.Debug("readyForJob: picked device: #%d (%s)", device.index, device.config.Name)

		time.AfterFunc(5*time.Second, func() {
			t.mu.Lock()
			delete(t.picked, device)
			t.mu.Unlock()
			t.app.Log.Debug("readyForJob: released device: #%d (%s)", device.index, device.config.Name)
		})
		return true
	}
}

// InitChannelStream はチャンネル全体のストリームを開始する。
func (t *Tuner) InitChannelStream(ch *core.ChannelItem, userReq UserRequest, output io.Writer) (*TSFilter, error) {
	var networkID *int
	services := ch.GetServices()
	if len(services) != 0 {
		networkID = apid.Ptr(services[0].NetworkID())
	}
	setting := &apid.StreamSetting{
		Channel:   channelConfig(ch),
		NetworkID: networkID,
		ParseEIT:  true,
	}
	return t.initTS(userReq, setting, ch, output)
}

// InitServiceStream はサービス単位のストリームを開始する。
func (t *Tuner) InitServiceStream(svc *core.ServiceItem, userReq UserRequest, output io.Writer) (*TSFilter, error) {
	setting := &apid.StreamSetting{
		Channel:   channelConfig(svc.Channel()),
		ServiceID: apid.Ptr(svc.ServiceID()),
		NetworkID: apid.Ptr(svc.NetworkID()),
		ParseEIT:  true,
	}
	return t.initTS(userReq, setting, svc.Channel(), output)
}

// InitProgramStream は番組単位のストリームを開始する。
func (t *Tuner) InitProgramStream(prog *apid.Program, userReq UserRequest, output io.Writer) (*TSFilter, error) {
	if t.app == nil || t.app.Service == nil {
		return nil, errors.New("no available services in program")
	}
	svc := t.app.Service.GetByNetworkID(prog.NetworkID, prog.ServiceID)
	if svc == nil {
		return nil, errors.New("no available services in program")
	}
	setting := &apid.StreamSetting{
		Channel:   channelConfig(svc.Channel()),
		ServiceID: apid.Ptr(prog.ServiceID),
		EventID:   apid.Ptr(prog.EventID),
		NetworkID: apid.Ptr(prog.NetworkID),
		ParseEIT:  true,
	}
	return t.initTS(userReq, setting, svc.Channel(), output)
}

// GetEPG はチャンネルの EPG を duration だけ回収する (Node 版 getEPG)。
func (t *Tuner) GetEPG(ctx context.Context, ch *core.ChannelItem, duration time.Duration) error {
	if duration <= 0 {
		ms := int64(1000 * 60 * 10)
		if t.app.Config.Server != nil && t.app.Config.Server.EPGRetrievalTime != nil {
			ms = int64(*t.app.Config.Server.EPGRetrievalTime)
		}
		duration = time.Duration(ms) * time.Millisecond
	}

	services := ch.GetServices()
	if len(services) == 0 {
		return errors.New("no available services in channel")
	}
	networkID := services[0].NetworkID()

	setting := &apid.StreamSetting{
		Channel:   channelConfig(ch),
		NetworkID: apid.Ptr(networkID),
		ParseEIT:  true,
	}
	tf, err := t.initTS(UserRequest{ID: "Mirakurun:getEPG()", Priority: -1, DisableDecoder: true}, setting, ch, nil)
	if err != nil {
		return err
	}

	closed := make(chan struct{})
	tf.OnClose(func() { close(closed) })

	var once sync.Once
	fin := func() { once.Do(func() { _ = tf.Close() }) }
	timer := time.AfterFunc(duration, fin)
	tf.OnEPGReady(fin)

	select {
	case <-closed:
	case <-ctx.Done():
		fin()
		<-closed
	}
	timer.Stop()
	return nil
}

// GetServices はチャンネルからサービス一覧を取得する (Node 版 getServices)。
func (t *Tuner) GetServices(ctx context.Context, ch *core.ChannelItem) ([]apid.Service, error) {
	setting := &apid.StreamSetting{
		Channel:  channelConfig(ch),
		ParseNIT: true,
		ParseSDT: true,
	}
	tf, err := t.initTS(UserRequest{ID: "Mirakurun:getServices()", Priority: -1, DisableDecoder: true}, setting, ch, nil)
	if err != nil {
		return nil, err
	}

	var mu sync.Mutex
	var network *networkInfo
	var sdtServices []sdtServiceInfo
	gotNetwork := false
	gotServices := false

	closed := make(chan struct{})
	tf.OnClose(func() { close(closed) })

	var once sync.Once
	finish := func() { once.Do(func() { _ = tf.Close() }) }

	tf.OnNetwork(func(n networkInfo) {
		mu.Lock()
		network = &n
		gotNetwork = true
		done := gotServices
		mu.Unlock()
		if done {
			finish()
		}
	})
	tf.OnServices(func(s []sdtServiceInfo) {
		mu.Lock()
		sdtServices = s
		gotServices = true
		done := gotNetwork
		mu.Unlock()
		if done {
			finish()
		}
	})

	timer := time.AfterFunc(20*time.Second, finish)

	select {
	case <-closed:
	case <-ctx.Done():
		finish()
		<-closed
	}
	timer.Stop()

	mu.Lock()
	haveNetwork := gotNetwork
	haveServices := gotServices
	net := network
	svcs := sdtServices
	mu.Unlock()

	if !haveNetwork || net == nil {
		return nil, errors.New("stream has closed before get network")
	}
	if !haveServices {
		return nil, errors.New("stream has closed before get services")
	}

	out := make([]apid.Service, 0, len(svcs))
	for _, s := range svcs {
		typ := s.Type
		logoID := s.LogoID
		item := apid.Service{
			ServiceID: s.ServiceID,
			NetworkID: s.NetworkID,
			Name:      s.Name,
			Type:      &typ,
			LogoID:    &logoID,
		}
		if net.RemoteControlKeyID != -1 {
			item.RemoteControlKeyID = apid.Ptr(net.RemoteControlKeyID)
		}
		item.Channel = &apid.ServiceChannel{Type: ch.Type, Channel: ch.Channel}
		out = append(out, item)
	}
	return out, nil
}

// initTS はチューナーを選び、TSFilter を生成して利用者を接続する (Node 版 _initTS)。
func (t *Tuner) initTS(user UserRequest, setting *apid.StreamSetting, ch *core.ChannelItem, dest io.Writer) (*TSFilter, error) {
	if t.app.Config.Server != nil && t.app.Config.Server.DisableEITParsing {
		setting.ParseEIT = false
	}

	devices := t.devicesByType(ch.Type)
	tryCount := 50

	for tryCount > 0 {
		device := t.pickTunerDevice(devices, ch, user.Priority)
		if device == nil {
			tryCount--
			if tryCount <= 0 {
				return nil, errors.New("no available tuners")
			}
			time.Sleep(250 * time.Millisecond)
			continue
		}

		var output io.Writer = dest
		if !user.DisableDecoder && device.decoderCommand() != "" {
			output = newTSDecoder(t.app, dest, device.decoderCommand())
		}

		tsmfRelTs := 0
		if setting.Channel != nil && setting.Channel.TsmfRelTs != nil {
			tsmfRelTs = *setting.Channel.TsmfRelTs
		}
		tf := NewTSFilter(t.app, TSFilterOptions{
			Output:    output,
			NetworkID: setting.NetworkID,
			ServiceID: setting.ServiceID,
			EventID:   setting.EventID,
			ParseNIT:  setting.ParseNIT,
			ParseSDT:  setting.ParseSDT,
			ParseEIT:  setting.ParseEIT,
			TsmfRelTs: tsmfRelTs,
		})

		du := &deviceUser{req: user, setting: setting, stream: tf}
		if err := device.startStream(du, tf, ch); err != nil {
			tf.End()
			return nil, err
		}
		return tf, nil
	}
	return nil, errors.New("no available tuners")
}

// pickTunerDevice は Node 版 _pickTunerDevice と同じ順で利用可能なデバイスを選ぶ。
func (t *Tuner) pickTunerDevice(devices []*TunerDevice, channel *core.ChannelItem, priority int) *TunerDevice {
	// 1. 既存ストリームへ相乗り
	for _, d := range devices {
		if d.IsAvailable() && d.Channel() == channel {
			return d
		}
	}

	// 2. 新規に開始
	for _, d := range devices {
		if d.IsFree() {
			return d
		}
	}

	// 3. 既存を置き換え (利用者なし)
	for _, d := range devices {
		if d.IsAvailable() && len(d.Users()) == 0 {
			return d
		}
	}

	// 4. 既存から乗っ取り
	if priority >= 0 {
		sorted := make([]*TunerDevice, len(devices))
		copy(sorted, devices)
		sort.SliceStable(sorted, func(i, j int) bool {
			return sorted[i].GetPriority() < sorted[j].GetPriority()
		})
		for _, d := range sorted {
			if d.IsUsing() && d.GetPriority() < priority {
				return d
			}
		}
	}

	return nil
}

// devicesByType は指定種別を扱えるデバイスを返す。
func (t *Tuner) devicesByType(typ apid.ChannelType) []*TunerDevice {
	t.mu.Lock()
	defer t.mu.Unlock()
	var devices []*TunerDevice
	for _, d := range t.devices {
		if typeIncluded(d.config.Types, typ) {
			devices = append(devices, d)
		}
	}
	return devices
}

func channelConfig(ch *core.ChannelItem) *apid.ConfigChannelsItem {
	if ch == nil {
		return nil
	}
	item := &apid.ConfigChannelsItem{
		Name:        ch.Name,
		Type:        ch.Type,
		Channel:     ch.Channel,
		CommandVars: ch.CommandVars,
	}
	if ch.TsmfRelTs != 0 {
		item.TsmfRelTs = apid.Ptr(ch.TsmfRelTs)
	}
	return item
}

// sleepCtx は ctx を見ながら d だけ待つ。キャンセルされたら false を返す。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
