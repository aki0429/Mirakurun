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

package core

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/db"
	"github.com/aki0429/Mirakurun/go/internal/job"
)

// ServiceItem は Node 版 ServiceItem.ts の移植。
type ServiceItem struct {
	mu                 sync.Mutex
	app                *App
	channel            *ChannelItem
	networkID          int
	serviceID          int
	id                 int64
	name               string
	typ                *int
	logoID             *int
	remoteControlKeyID *int
	epgReady           bool
	epgUpdatedAt       int64
}

// GetServiceItemID は Node 版 ServiceItem.getId と同じ id を返す。
// Node は `networkId + (serviceId / 100000).toFixed(5).slice(2)` を parseInt する。
func GetServiceItemID(networkID, serviceID int) int64 {
	return int64(networkID)*100000 + int64(serviceID)
}

func newServiceItem(
	app *App, channel *ChannelItem, networkID, serviceID int,
	name string, typ, logoID, remoteControlKeyID *int,
	epgReady bool, epgUpdatedAt int64,
) *ServiceItem {
	return &ServiceItem{
		app:                app,
		channel:            channel,
		networkID:          networkID,
		serviceID:          serviceID,
		id:                 GetServiceItemID(networkID, serviceID),
		name:               name,
		typ:                typ,
		logoID:             logoID,
		remoteControlKeyID: remoteControlKeyID,
		epgReady:           epgReady,
		epgUpdatedAt:       epgUpdatedAt,
	}
}

// ID はサービス ID を返す。
func (s *ServiceItem) ID() int64 { return s.id }

// NetworkID は networkId を返す。
func (s *ServiceItem) NetworkID() int { return s.networkID }

// ServiceID は serviceId を返す。
func (s *ServiceItem) ServiceID() int { return s.serviceID }

// Name はサービス名を返す (未設定なら "")。
func (s *ServiceItem) Name() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.name
}

// SetName は名前を更新し、変化があれば保存予約と update イベントを行う。
func (s *ServiceItem) SetName(name string) {
	s.mu.Lock()
	if s.name == name {
		s.mu.Unlock()
		return
	}
	s.name = name
	s.mu.Unlock()
	s.app.Service.save()
	s.updated()
}

// Type はサービス種別を返す (未設定は nil)。
func (s *ServiceItem) Type() *int { s.mu.Lock(); defer s.mu.Unlock(); return s.typ }

// SetType は種別を更新する。
func (s *ServiceItem) SetType(t *int) {
	s.mu.Lock()
	if intPtrEq(s.typ, t) {
		s.mu.Unlock()
		return
	}
	s.typ = t
	s.mu.Unlock()
	s.app.Service.save()
	s.updated()
}

// LogoID はロゴ ID を返す (未設定は nil)。
func (s *ServiceItem) LogoID() *int { s.mu.Lock(); defer s.mu.Unlock(); return s.logoID }

// SetLogoID はロゴ ID を更新する。
func (s *ServiceItem) SetLogoID(v *int) {
	s.mu.Lock()
	if intPtrEq(s.logoID, v) {
		s.mu.Unlock()
		return
	}
	s.logoID = v
	s.mu.Unlock()
	s.app.Service.save()
	s.updated()
}

// RemoteControlKeyID はリモコンキー ID を返す (未設定は nil)。
func (s *ServiceItem) RemoteControlKeyID() *int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remoteControlKeyID
}

// SetRemoteControlKeyID はリモコンキー ID を更新する。
func (s *ServiceItem) SetRemoteControlKeyID(v *int) {
	s.mu.Lock()
	if intPtrEq(s.remoteControlKeyID, v) {
		s.mu.Unlock()
		return
	}
	s.remoteControlKeyID = v
	s.mu.Unlock()
	s.app.Service.save()
	s.updated()
}

// EPGReady は EPG 取得済みフラグを返す。
func (s *ServiceItem) EPGReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.epgReady
}

// SetEPGReady は EPG 取得済みフラグを更新する。
func (s *ServiceItem) SetEPGReady(v bool) {
	s.mu.Lock()
	if s.epgReady == v {
		s.mu.Unlock()
		return
	}
	s.epgReady = v
	s.mu.Unlock()
	s.app.Service.save()
	s.updated()
}

// EPGUpdatedAt は EPG 最終更新時刻 (ms) を返す。
func (s *ServiceItem) EPGUpdatedAt() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.epgUpdatedAt
}

// SetEPGUpdatedAt は EPG 最終更新時刻を更新する。
func (s *ServiceItem) SetEPGUpdatedAt(t int64) {
	s.mu.Lock()
	if s.epgUpdatedAt == t {
		s.mu.Unlock()
		return
	}
	s.epgUpdatedAt = t
	s.mu.Unlock()
	s.app.Service.save()
	s.updated()
}

// Channel はチャンネルを返す。
func (s *ServiceItem) Channel() *ChannelItem { return s.channel }

// Export は Node 版 ServiceItem#export と同じ JSON 形状を返す。
func (s *ServiceItem) Export() apid.Service {
	s.mu.Lock()
	out := apid.Service{
		ID:           s.id,
		ServiceID:    s.serviceID,
		NetworkID:    s.networkID,
		Name:         s.name,
		Type:         s.typ,
		LogoID:       s.logoID,
		EPGReady:     apid.Ptr(s.epgReady),
		EPGUpdatedAt: apid.Ptr(s.epgUpdatedAt),
	}
	rcKey := s.remoteControlKeyID
	ch := s.channel
	s.mu.Unlock()

	out.RemoteControlKeyID = rcKey
	if ch != nil {
		out.Channel = &apid.ServiceChannel{Type: ch.Type, Channel: ch.Channel}
	}
	return out
}

// GetOrder は Node 版 ServiceItem#getOrder と同じ並び順キーを返す。
func (s *ServiceItem) GetOrder() int64 {
	order := ""
	switch s.channel.Type {
	case apid.ChannelTypeGR:
		order = "1"
	case apid.ChannelTypeBS:
		order = "2"
	case apid.ChannelTypeCS:
		order = "3"
	case apid.ChannelTypeSKY:
		order = "4"
	}
	rc := s.RemoteControlKeyID()
	if rc != nil && *rc != 0 {
		order += strconv.Itoa(100 + *rc)
	} else {
		order += "200"
	}
	order += strconv.Itoa(10000 + s.serviceID)
	n, _ := strconv.ParseInt(order, 10, 64)
	return n
}

func (s *ServiceItem) updated() {
	if s.app.Event != nil {
		s.app.Event.Emit(apid.EventResourceService, apid.EventTypeUpdate, s.Export())
	}
}

// ServiceStore は Node 版 Service.ts の移植。
type ServiceStore struct {
	mu        sync.RWMutex
	app       *App
	items     []*ServiceItem
	saveTimer job.Timer
}

func newServiceStore(a *App) *ServiceStore {
	return &ServiceStore{app: a}
}

// Items はサービス一覧のコピーを返す。
func (s *ServiceStore) Items() []*ServiceItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]*ServiceItem(nil), s.items...)
}

// Len はサービス数を返す。
func (s *ServiceStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// Add は id 重複が無ければ追加し、create イベントと保存予約を行う。
func (s *ServiceStore) Add(item *ServiceItem) {
	if s.GetByID(item.id) != nil {
		return
	}
	s.mu.Lock()
	s.items = append(s.items, item)
	s.mu.Unlock()

	if s.app.Event != nil {
		s.app.Event.Emit(apid.EventResourceService, apid.EventTypeCreate, item.Export())
	}
	s.save()
}

// GetByID は id 一致のサービスを返す。
func (s *ServiceStore) GetByID(id int64) *ServiceItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, it := range s.items {
		if it.id == id {
			return it
		}
	}
	return nil
}

// GetByNetworkID は (networkId, serviceId) 一致のサービスを返す。
func (s *ServiceStore) GetByNetworkID(networkID, serviceID int) *ServiceItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, it := range s.items {
		if it.networkID == networkID && it.serviceID == serviceID {
			return it
		}
	}
	return nil
}

// ExistsByID は id 存在を返す。
func (s *ServiceStore) ExistsByID(id int64) bool { return s.GetByID(id) != nil }

// ExistsByNetworkID は (networkId, serviceId) 存在を返す。
func (s *ServiceStore) ExistsByNetworkID(networkID, serviceID int) bool {
	return s.GetByNetworkID(networkID, serviceID) != nil
}

// FindByChannel はチャンネル一致のサービス一覧を返す (ポインタ一致)。
func (s *ServiceStore) FindByChannel(channel *ChannelItem) []*ServiceItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []*ServiceItem{}
	for _, it := range s.items {
		if it.channel == channel {
			out = append(out, it)
		}
	}
	return out
}

// FindByNetworkID は networkId 一致のサービス一覧を返す。
func (s *ServiceStore) FindByNetworkID(networkID int) []*ServiceItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []*ServiceItem{}
	for _, it := range s.items {
		if it.networkID == networkID {
			out = append(out, it)
		}
	}
	return out
}

// FindByNetworkIDWithLogoID は (networkId, logoId) 一致のサービス一覧を返す。
func (s *ServiceStore) FindByNetworkIDWithLogoID(networkID, logoID int) []*ServiceItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []*ServiceItem{}
	for _, it := range s.items {
		if it.networkID == networkID && it.logoID != nil && *it.logoID == logoID {
			out = append(out, it)
		}
	}
	return out
}

// NetworkIDs は登録済みサービスの networkId を初出順で返す (Node の Set 相当)。
func (s *ServiceStore) NetworkIDs() []int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[int]bool{}
	out := []int{}
	for _, it := range s.items {
		if !seen[it.networkID] {
			seen[it.networkID] = true
			out = append(out, it.networkID)
		}
	}
	return out
}

// save は 10 秒後の保存を予約する (既存予約は置き換える)。
func (s *ServiceStore) save() {
	s.mu.Lock()
	if s.saveTimer != nil {
		s.saveTimer.Stop()
	}
	s.saveTimer = s.app.Clock.AfterFunc(10*time.Second, s.persist)
	s.mu.Unlock()
}

func (s *ServiceStore) persist() {
	s.mu.RLock()
	exports := make([]apid.Service, 0, len(s.items))
	for _, it := range s.items {
		exports = append(exports, it.Export())
	}
	s.mu.RUnlock()

	s.app.Log.Debug("saving services...")
	if err := db.SaveServices(s.app.Config.ServicesDBPath, exports, s.app.Config.ChannelsIntegrity); err != nil {
		s.app.Log.Error("failed to save services: %s", err)
	}
}

// Load は services.json を読み込み、logoData の移行と Job 初期化予約を行う。
func (s *ServiceStore) Load() error {
	s.app.Log.Debug("loading services...")

	updated := false
	services, err := db.LoadServices(s.app.Config.ServicesDBPath, s.app.Config.ChannelsIntegrity)
	if err != nil {
		return err
	}

	for i := range services {
		svc := &services[i]

		if svc.Channel == nil {
			updated = true
			continue
		}
		channelItem := s.app.Channel.Get(svc.Channel.Type, svc.Channel.Channel)
		if channelItem == nil {
			updated = true
			continue
		}
		if svc.NetworkID == 0 || svc.ServiceID == 0 {
			updated = true
			continue
		}

		// 旧形式 logoData の移行
		if svc.LogoData != "" {
			logoID := derefInt(svc.LogoID)
			path := GetLogoDataPath(s.app.Config.LogoDataDirPath, svc.NetworkID, logoID)
			s.app.Log.Warn("migrating deprecated property `logoData` to file `%s` in service#%d (%s) db", path, svc.ID, svc.Name)

			if raw, derr := base64.StdEncoding.DecodeString(svc.LogoData); derr == nil {
				if serr := SaveLogoData(s.app.Config.LogoDataDirPath, svc.NetworkID, logoID, raw); serr != nil {
					s.app.Log.Error("failed to save logo data: %s", serr)
				}
			}
			// 重複の logoData を削除
			for j := range services {
				if services[j].NetworkID == svc.NetworkID && derefInt(services[j].LogoID) == logoID {
					services[j].LogoData = ""
				}
			}
			updated = true
		}

		s.Add(newServiceItem(
			s.app, channelItem, svc.NetworkID, svc.ServiceID,
			svc.Name, svc.Type, svc.LogoID, svc.RemoteControlKeyID,
			derefBool(svc.EPGReady), derefInt64(svc.EPGUpdatedAt),
		))
	}

	if updated {
		s.save()
	}

	s.app.Clock.AfterFunc(10*time.Second, s.initJobs)
	return nil
}

// initJobs は Node 版 Service#_initJobs の移植 (10 秒後に呼ばれる)。
func (s *ServiceStore) initJobs() {
	s.app.Log.Debug("init service jobs...")

	// チャンネル設定に serviceId があるサービスの確認ジョブを積む
	for i := range s.app.Config.Channels {
		cc := &s.app.Config.Channels[i]
		if cc.IsDisabled || cc.ServiceID == nil || *cc.ServiceID == 0 {
			continue
		}
		channel := s.app.Channel.Get(cc.Type, cc.Channel)
		if channel == nil {
			continue
		}
		serviceID := *cc.ServiceID
		if hasServiceID(s.FindByChannel(channel), serviceID) {
			continue
		}
		s.queueCheckToAdd(channel, serviceID)
	}

	// サービス未検出チャンネルの走査
	s.app.Job.Add(job.Item{
		Key:  "Service.Add.Scan.Find-Channels",
		Name: "Service Add Scan [Find Targets]",
		Fn: func(ctx context.Context) error {
			for _, channel := range s.app.Channel.Items() {
				if len(s.FindByChannel(channel)) > 0 {
					continue
				}
				s.queueScanToAdd(channel)
			}
			return nil
		},
		ReadyFn: func(ctx context.Context) (bool, error) {
			for {
				if !s.hasPendingCheckJobs() {
					return true, nil
				}
				if err := job.Sleep(ctx, s.app.Clock, time.Second); err != nil {
					return false, nil
				}
			}
		},
	})

	// 毎日 6:05 のサービス更新スキャン
	updaterSchedule := "5 6 * * *"
	_ = s.app.Job.AddSchedule(job.ScheduleItem{
		Key:      "Service.Updater.Add-Schedule",
		Schedule: updaterSchedule,
		Job: job.Item{
			Key:  "Service.Updater.Add-Schedule",
			Name: "Service Updater [Add Schedule]",
			Fn: func(ctx context.Context) error {
				_ = s.app.Job.AddSchedule(job.ScheduleItem{
					Key:      "Service.Updater",
					Schedule: updaterSchedule,
					Job: job.Item{
						Key:  "Service.Updater",
						Name: "Service Updater",
						Fn: func(ctx context.Context) error {
							for _, channel := range s.app.Channel.Items() {
								if len(s.FindByChannel(channel)) == 0 {
									continue
								}
								s.queueScanToUpdate(channel)
							}
							return nil
						},
					},
				})
				return nil
			},
		},
	})
}

// hasPendingCheckJobs は Service.Add.Check.* ジョブが未完了か返す。
func (s *ServiceStore) hasPendingCheckJobs() bool {
	for _, j := range s.app.Job.Jobs() {
		if j.Status != "finished" && contains(j.Key, "Service.Add.Check.") {
			return true
		}
	}
	return false
}

func (s *ServiceStore) queueCheckToAdd(channel *ChannelItem, serviceID int) {
	s.app.Job.Add(job.Item{
		Key:         fmt.Sprintf("Service.Add.Check.%s.%s.%d", channel.Type, channel.Channel, serviceID),
		Name:        fmt.Sprintf("Service Add Check %s/%s/%d", channel.Type, channel.Channel, serviceID),
		Fn:          func(ctx context.Context) error { return s.checkToAdd(ctx, channel, serviceID) },
		ReadyFn:     func(ctx context.Context) (bool, error) { return s.app.Tuner.ReadyForJob(ctx, channel), nil },
		RetryOnFail: apid.Ptr(true),
		RetryMax:    apid.Ptr(240),
		RetryDelay:  apid.Ptr(3 * 60 * 1000),
	})
}

func (s *ServiceStore) queueScanToAdd(channel *ChannelItem) {
	s.app.Job.Add(job.Item{
		Key:         fmt.Sprintf("Service.Add.Scan.%s.%s", channel.Type, channel.Channel),
		Name:        fmt.Sprintf("Service Add Scan %s/%s", channel.Type, channel.Channel),
		Fn:          func(ctx context.Context) error { return s.scan(ctx, channel, true) },
		ReadyFn:     func(ctx context.Context) (bool, error) { return s.app.Tuner.ReadyForJob(ctx, channel), nil },
		RetryOnFail: apid.Ptr(true),
		RetryMax:    apid.Ptr(240),
		RetryDelay:  apid.Ptr(3 * 60 * 1000),
	})
}

func (s *ServiceStore) queueScanToUpdate(channel *ChannelItem) {
	s.app.Job.Add(job.Item{
		Key:     fmt.Sprintf("Service.Update.Scan.%s.%s", channel.Type, channel.Channel),
		Name:    fmt.Sprintf("Service Update Scan %s/%s", channel.Type, channel.Channel),
		Fn:      func(ctx context.Context) error { return s.scan(ctx, channel, false) },
		ReadyFn: func(ctx context.Context) (bool, error) { return s.app.Tuner.ReadyForJob(ctx, channel), nil },
	})
}

func (s *ServiceStore) checkToAdd(ctx context.Context, channel *ChannelItem, serviceID int) error {
	s.app.Log.Info("ChannelItem#'%s' serviceId=%d check has started", channel.Name, serviceID)

	services, err := s.app.Tuner.GetServices(ctx, channel)
	if err != nil {
		s.app.Log.Warn("ChannelItem#'%s' serviceId=%d check has failed [%s]", channel.Name, serviceID, err)
		return errors.New("Service check failed")
	}

	var found *apid.Service
	for i := range services {
		if services[i].ServiceID == serviceID {
			found = &services[i]
			break
		}
	}
	if found == nil {
		s.app.Log.Warn("ChannelItem#'%s' serviceId=%d check has failed [no service]", channel.Name, serviceID)
		// 1 時間後に再確認
		s.app.Clock.AfterFunc(time.Hour, func() { s.queueCheckToAdd(channel, serviceID) })
		return nil
	}

	s.Add(newServiceItem(s.app, channel, found.NetworkID, found.ServiceID, found.Name,
		found.Type, found.LogoID, found.RemoteControlKeyID, false, 0))
	s.app.Log.Info("ChannelItem#'%s' serviceId=%d check has finished", channel.Name, serviceID)
	return nil
}

func (s *ServiceStore) scan(ctx context.Context, channel *ChannelItem, add bool) error {
	s.app.Log.Info("ChannelItem#'%s' service scan has started", channel.Name)

	services, err := s.app.Tuner.GetServices(ctx, channel)
	if err != nil {
		s.app.Log.Warn("ChannelItem#'%s' service scan has failed [%s]", channel.Name, err)
		return errors.New("Service scan failed")
	}

	for i := range services {
		svc := &services[i]
		item := s.GetByNetworkID(svc.NetworkID, svc.ServiceID)
		if item != nil {
			item.SetName(svc.Name)
			item.SetType(svc.Type)
			if svc.LogoID != nil && *svc.LogoID > -1 {
				item.SetLogoID(svc.LogoID)
			}
			item.SetRemoteControlKeyID(svc.RemoteControlKeyID)
		} else if add {
			s.Add(newServiceItem(s.app, channel, svc.NetworkID, svc.ServiceID, svc.Name,
				svc.Type, svc.LogoID, svc.RemoteControlKeyID, false, 0))
		}
	}

	s.app.Log.Info("ChannelItem#'%s' service scan has finished", channel.Name)
	return nil
}

// ---- logo data ヘルパ (Node 版 Service の static メソッド相当) ----

// GetLogoDataPath はロゴファイルのパスを返す。
func GetLogoDataPath(dir string, networkID, logoID int) string {
	return filepath.Join(dir, fmt.Sprintf("%d_%d.png", networkID, logoID))
}

// GetLogoDataMTime はロゴファイルの mtime (ms) を返す。無ければ 0。
func GetLogoDataMTime(dir string, networkID, logoID int) int64 {
	if logoID < 0 {
		return 0
	}
	st, err := os.Stat(GetLogoDataPath(dir, networkID, logoID))
	if err != nil {
		return 0
	}
	return st.ModTime().UnixMilli()
}

// IsLogoDataExists はロゴファイルが存在するか返す。
func IsLogoDataExists(dir string, networkID, logoID int) bool {
	if logoID < 0 {
		return false
	}
	st, err := os.Stat(GetLogoDataPath(dir, networkID, logoID))
	return err == nil && st.Mode().IsRegular()
}

// LoadLogoData はロゴファイルを読む。無ければ nil。
func LoadLogoData(dir string, networkID, logoID int) []byte {
	if logoID < 0 {
		return nil
	}
	data, err := os.ReadFile(GetLogoDataPath(dir, networkID, logoID))
	if err != nil {
		return nil
	}
	return data
}

// SaveLogoData はロゴファイルを書き出す (親ディレクトリを作成して再試行する)。
func SaveLogoData(dir string, networkID, logoID int, data []byte) error {
	path := GetLogoDataPath(dir, networkID, logoID)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
			return mkErr
		}
		return os.WriteFile(path, data, 0o644)
	}
	return nil
}

func hasServiceID(items []*ServiceItem, serviceID int) bool {
	for _, it := range items {
		if it.serviceID == serviceID {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func intPtrEq(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func derefBool(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
