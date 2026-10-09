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
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/job"
)

// ChannelItem は Node 版 ChannelItem.ts の移植。
type ChannelItem struct {
	Name        string
	Type        apid.ChannelType
	Channel     string
	TsmfRelTs   int
	CommandVars map[string]any

	app *App
}

// GetServices はこのチャンネルに紐づくサービス一覧を返す。
func (c *ChannelItem) GetServices() []*ServiceItem { return c.app.Service.FindByChannel(c) }

// ChannelStore は Node 版 Channel.ts の移植。
type ChannelStore struct {
	mu      sync.RWMutex
	app     *App
	items   []*ChannelItem
	startup bool
}

func newChannelStore(a *App) *ChannelStore {
	c := &ChannelStore{app: a, startup: true}
	c.load()

	if a.Config.Server == nil || a.Config.Server.DisableEITParsing != true {
		epgJob := job.Item{
			Key:  "EPG.Gatherer",
			Name: "EPG Gatherer",
			Fn:   func(ctx context.Context) error { return c.epgGatherer(ctx) },
		}
		oneShot := epgJob
		oneShot.ReadyFn = func(ctx context.Context) (bool, error) {
			if err := job.Sleep(ctx, a.Clock, time.Minute); err != nil {
				return false, nil
			}
			return true, nil
		}
		a.Job.Add(oneShot)

		schedule := "20,50 * * * *"
		if a.Config.Server != nil && a.Config.Server.EPGGatheringJobSchedule != "" {
			schedule = a.Config.Server.EPGGatheringJobSchedule
		}
		_ = a.Job.AddSchedule(job.ScheduleItem{Key: epgJob.Key, Schedule: schedule, Job: epgJob})
	}
	return c
}

// Items はチャンネル一覧のコピーを返す。
func (c *ChannelStore) Items() []*ChannelItem {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]*ChannelItem(nil), c.items...)
}

// Len はチャンネル数を返す。
func (c *ChannelStore) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// Add は重複 (type, channel) が無ければ追加する。
func (c *ChannelStore) Add(item *ChannelItem) {
	if c.Get(item.Type, item.Channel) != nil {
		return
	}
	c.mu.Lock()
	c.items = append(c.items, item)
	c.mu.Unlock()
}

// Get は (type, channel) に一致するチャンネルを返す。無ければ nil。
func (c *ChannelStore) Get(typ apid.ChannelType, channel string) *ChannelItem {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, it := range c.items {
		if it.Channel == channel && it.Type == typ {
			return it
		}
	}
	return nil
}

// FindByType は指定種別のチャンネル一覧を返す。
func (c *ChannelStore) FindByType(typ apid.ChannelType) []*ChannelItem {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := []*ChannelItem{}
	for _, it := range c.items {
		if it.Type == typ {
			out = append(out, it)
		}
	}
	return out
}

// load は Node 版 Channel#_load の検証・移行を config.channels に対して行う。
// 検証で弾かれた項目はログを出してスキップし、生き残った項目だけ ChannelItem 化する。
func (c *ChannelStore) load() {
	log := c.app.Log
	channels := c.app.Config.Channels

	for i := range channels {
		ch := &channels[i]

		if ch.Name == "" {
			log.Error("invalid type of property `name` in channel#%d configuration", i)
			continue
		}
		if ch.Type != apid.ChannelTypeGR && ch.Type != apid.ChannelTypeBS && ch.Type != apid.ChannelTypeCS && ch.Type != apid.ChannelTypeSKY {
			log.Error("invalid type of property `type` in channel#%d (%s) configuration", i, ch.Name)
			continue
		}
		if ch.Channel == "" {
			log.Error("invalid type of property `channel` in channel#%d (%s) configuration", i, ch.Name)
			continue
		}

		if ch.CommandVars == nil {
			ch.CommandVars = map[string]any{}
		}
		if ch.Satelite != "" && ch.Satellite == "" {
			log.Warn("renaming deprecated property name `satelite` to `satellite` in channel#%d (%s) configuration", i, ch.Name)
			ch.Satellite = ch.Satelite
		}
		if ch.Satellite != "" {
			if _, ok := ch.CommandVars["satellite"]; !ok {
				ch.CommandVars["satellite"] = ch.Satellite
			}
		}
		if ch.Space != nil {
			if _, ok := ch.CommandVars["space"]; !ok {
				ch.CommandVars["space"] = *ch.Space
			}
		}
		if ch.Freq != nil {
			if _, ok := ch.CommandVars["freq"]; !ok {
				ch.CommandVars["freq"] = *ch.Freq
			}
		}
		if ch.Polarity != "" {
			if _, ok := ch.CommandVars["polarity"]; !ok {
				ch.CommandVars["polarity"] = ch.Polarity
			}
		}
		for key, v := range ch.CommandVars {
			if !isStringOrNumber(v) {
				log.Error("invalid type of property `commandVars.%s` in channel#%d (%s) configuration", key, i, ch.Name)
				delete(ch.CommandVars, key)
			}
		}

		if ch.IsDisabled {
			continue
		}
		if c.app.Tuner == nil || !c.app.Tuner.TypeExists(ch.Type) {
			continue
		}

		if c.Get(ch.Type, ch.Channel) == nil {
			if ch.ServiceID != nil && *ch.ServiceID != 0 {
				ch.Name = fmt.Sprintf("%s:%s", ch.Type, ch.Channel)
			}
			c.Add(&ChannelItem{
				Name:        ch.Name,
				Type:        ch.Type,
				Channel:     ch.Channel,
				TsmfRelTs:   derefInt(ch.TsmfRelTs),
				CommandVars: ch.CommandVars,
				app:         c.app,
			})
		}
	}
}

// epgGatherer は Node 版 Channel#_epgGatherer の移植。
func (c *ChannelStore) epgGatherer(ctx context.Context) error {
	c.mu.Lock()
	startup := c.startup
	if c.startup {
		c.startup = false
	}
	c.mu.Unlock()

	networkIDs := c.app.Service.NetworkIDs()

	for _, networkID := range networkIDs {
		services := c.app.Service.FindByNetworkID(networkID)
		if len(services) == 0 {
			continue
		}
		service := services[0]

		c.app.Job.Add(job.Item{
			Key:          fmt.Sprintf("EPG.Gather.NID.%d", networkID),
			Name:         fmt.Sprintf("EPG Gather Network#%d", networkID),
			IsRerunnable: apid.Ptr(true),
			Fn: func(ictx context.Context) error {
				c.app.Log.Info("Network#%d EPG gathering has started", networkID)
				if err := c.app.Tuner.GetEPG(ictx, service.Channel(), 0); err != nil {
					c.app.Log.Warn("Network#%d EPG gathering has failed [%s]", networkID, err)
					return errors.New("EPG gathering failed")
				}
				c.app.Log.Info("Network#%d EPG gathering has finished", networkID)
				return nil
			},
			ReadyFn: func(rctx context.Context) (bool, error) {
				if err := job.Sleep(rctx, c.app.Clock, 100*time.Millisecond); err != nil {
					return false, nil
				}
				if c.app.Status.IsEPGGathering(networkID) {
					c.app.Log.Info("Network#%d EPG gathering is already in progress on another stream", networkID)
					return false, nil
				}
				if service.EPGReady() {
					now := c.app.Clock.Now().UnixMilli()
					if startup && now-service.EPGUpdatedAt() < 10*60*1000 {
						c.app.Log.Info("Network#%d EPG gathering has skipped because EPG is already up to date (in 10 mins)", networkID)
						return false, nil
					}
					if now-service.EPGUpdatedAt() > 12*60*60*1000 {
						c.app.Log.Info("Network#%d EPG gathering is resuming forcibly because reached maximum pause time (12 hours)", networkID)
						service.SetEPGReady(false)
					} else {
						currentPrograms := filterCurrentPrograms(c.app.Program.FindByNetworkIDAndTime(networkID, now))
						if len(currentPrograms) == 0 {
							networkPrograms := c.app.Program.FindByNetworkID(networkID)
							if len(networkPrograms) > 0 {
								c.app.Log.Info("Network#%d EPG gathering has skipped because broadcast is off", networkID)
								return false, nil
							}
							service.SetEPGReady(false)
						}
					}
				}
				return c.app.Tuner.ReadyForJob(rctx, service.Channel()), nil
			},
		})
	}
	return nil
}

// filterCurrentPrograms は放送休止等を除いた「放送中」番組だけを残す (Node 版の filter 相当)。
func filterCurrentPrograms(programs []*apid.Program) []*apid.Program {
	out := make([]*apid.Program, 0, len(programs))
	for _, p := range programs {
		if p.Name == nil || *p.Name == "" {
			continue
		}
		if strings.TrimSpace(*p.Name) == "放送休止" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func isStringOrNumber(v any) bool {
	switch v.(type) {
	case string, int, int64, int32, uint, uint64, float64, float32:
		return true
	default:
		return false
	}
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
