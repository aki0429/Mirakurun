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
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/db"
	"github.com/aki0429/Mirakurun/go/internal/job"
)

// WhereQueryError は `$where` を含むクエリを拒否するエラー。
// HTTP 層はこれを見て 400 を返す (Node 版 common.WhereQueryError 相当)。
type WhereQueryError struct{}

// Error はエラーメッセージを返す。
func (e *WhereQueryError) Error() string { return "$where is not allowed" }

// GetProgramItemID は Node 版 getProgramItemId と同じ id を返す。
func GetProgramItemID(networkID, serviceID, eventID int) int64 {
	id := fmt.Sprintf("%d%05d%05d", networkID, serviceID, eventID)
	n, _ := strconv.ParseInt(id, 10, 64)
	return n
}

// ProgramPatch は Node 版 Program#set に渡される Partial<Program> 相当。
// nil ポインタ / nil スライスはキー未指定 (undefined) を意味する。
type ProgramPatch struct {
	StartAt      *int64
	Duration     *int64
	IsFree       *bool
	PF           *bool
	IsPresent    *bool
	IsFollowing  *bool
	Name         *string
	Description  *string
	Extended     map[string]string
	Video        *apid.ProgramVideo
	Genres       []apid.ProgramGenre
	Audios       []apid.ProgramAudio
	Series       *apid.ProgramSeries
	RelatedItems []apid.ProgramRelatedItem
}

// ProgramStore は Node 版 Program.ts の移植。
type ProgramStore struct {
	mu             sync.Mutex
	app            *App
	itemMap        map[int64]*apid.Program
	itemMapDeleted map[int64]*apid.Program
	order          []int64

	emitOrder   []*apid.Program
	emitType    map[*apid.Program]apid.EventType
	emitRunning bool

	saveTimer job.Timer
	emitTimer job.Timer
}

func newProgramStore(a *App) *ProgramStore {
	p := &ProgramStore{
		app:            a,
		itemMap:        map[int64]*apid.Program{},
		itemMapDeleted: map[int64]*apid.Program{},
		emitType:       map[*apid.Program]apid.EventType{},
	}

	gcJob := job.Item{
		Key:  "Program.GC",
		Name: "Program GC",
		Fn:   func(ctx context.Context) error { p.gc(); return nil },
	}
	oneShot := gcJob
	oneShot.ReadyFn = func(ctx context.Context) (bool, error) {
		if err := job.Sleep(ctx, a.Clock, 5*time.Second); err != nil {
			return false, nil
		}
		return true, nil
	}
	a.Job.Add(oneShot)

	schedule := "45 * * * *"
	if a.Config.Server != nil && a.Config.Server.ProgramGCJobSchedule != "" {
		schedule = a.Config.Server.ProgramGCJobSchedule
	}
	_ = a.Job.AddSchedule(job.ScheduleItem{Key: "Program.GC", Schedule: schedule, Job: gcJob})

	return p
}

// Len は保持している番組数を返す (status.epg.storedEvents 相当)。
func (p *ProgramStore) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.itemMap)
}

// ItemMap は id→番組のコピーを返す。
func (p *ProgramStore) ItemMap() map[int64]*apid.Program {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[int64]*apid.Program, len(p.itemMap))
	for k, v := range p.itemMap {
		out[k] = v
	}
	return out
}

// Add は番組を追加する。firstAdd が true のときは衝突解決と create イベントを行わない (読み込み用)。
func (p *ProgramStore) Add(item *apid.Program, firstAdd bool) {
	p.mu.Lock()
	if _, ok := p.itemMap[item.ID]; ok {
		p.mu.Unlock()
		return
	}
	delete(p.itemMapDeleted, item.ID)

	if !firstAdd {
		p.findAndRemoveConflictsLocked(item)
	}
	p.itemMap[item.ID] = item
	p.order = append(p.order, item.ID)

	if !firstAdd {
		p.setEmitLocked(item, apid.EventTypeCreate)
	}
	p.mu.Unlock()

	p.save()
}

// Get は id 一致の番組を返す。
func (p *ProgramStore) Get(id int64) *apid.Program {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.itemMap[id]
}

// Set は Partial 更新を適用し、実変化があれば update イベントと保存を行う。
func (p *ProgramStore) Set(id int64, patch ProgramPatch) {
	p.mu.Lock()
	item := p.itemMap[id]
	if item == nil {
		if deleted := p.itemMapDeleted[id]; deleted != nil {
			item = deleted
			p.itemMap[item.ID] = item
			delete(p.itemMapDeleted, item.ID)
			p.setEmitLocked(item, apid.EventTypeCreate)
			p.mu.Unlock()
			p.app.Log.Debug("ProgramItem#%d has recovered from the logically-deleted store", item.ID)
			p.save()
			// fallthrough: still apply patch below
			p.mu.Lock()
		}
	}
	if item != nil && applyPatch(item, patch) {
		if patch.StartAt != nil || patch.Duration != nil {
			p.findAndRemoveConflictsLocked(item)
		}
		p.setEmitLocked(item, apid.EventTypeUpdate)
		p.mu.Unlock()
		p.save()
		return
	}
	p.mu.Unlock()
}

// Remove は番組を削除する。logicallyDelete が true なら論理削除 (復元可能) にする。
func (p *ProgramStore) Remove(id int64, logicallyDelete bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if logicallyDelete {
		if item := p.itemMap[id]; item != nil {
			p.itemMapDeleted[id] = item
			delete(p.itemMap, id)
			p.mu.Unlock()
			p.save()
			p.mu.Lock()
		}
		return
	}
	if _, ok := p.itemMap[id]; ok {
		delete(p.itemMap, id)
		p.mu.Unlock()
		p.save()
		p.mu.Lock()
	}
}

// Exists は番組が存在するか返す。
func (p *ProgramStore) Exists(id int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.itemMap[id]
	return ok
}

// IsLogicallyDeleted は論理削除済みか返す。
func (p *ProgramStore) IsLogicallyDeleted(id int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.itemMapDeleted[id]
	return ok
}

// FindByQuery は sift 的な等値クエリを適用する。$where は WhereQueryError を返す。
func (p *ProgramStore) FindByQuery(query map[string]string) ([]*apid.Program, error) {
	if err := rejectWhere(query); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	out := []*apid.Program{}
	for _, item := range p.valuesLocked() {
		if matchQuery(item, query) {
			out = append(out, item)
		}
	}
	return out, nil
}

// FindByNetworkID は networkId 一致の番組一覧を返す。
func (p *ProgramStore) FindByNetworkID(networkID int) []*apid.Program {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []*apid.Program{}
	for _, item := range p.valuesLocked() {
		if item.NetworkID == networkID {
			out = append(out, item)
		}
	}
	return out
}

// FindByNetworkIDAndTime は networkId かつ time が放送中の番組を返す。
func (p *ProgramStore) FindByNetworkIDAndTime(networkID int, t int64) []*apid.Program {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []*apid.Program{}
	for _, item := range p.valuesLocked() {
		if item.NetworkID == networkID && item.StartAt <= t && item.StartAt+item.Duration > t {
			out = append(out, item)
		}
	}
	return out
}

// FindByNetworkIDAndReplace は networkId の番組を programs で置き換える。
func (p *ProgramStore) FindByNetworkIDAndReplace(networkID int, programs []*apid.Program) {
	p.mu.Lock()
	count := 0
	values := p.valuesLocked()
	for i := len(values) - 1; i >= 0; i-- {
		item := values[i]
		if item.NetworkID == networkID {
			delete(p.itemMap, item.ID)
			count--
		}
	}
	for _, program := range programs {
		if _, ok := p.itemMap[program.ID]; ok {
			continue
		}
		delete(p.itemMapDeleted, program.ID)
		p.itemMap[program.ID] = program
		p.order = append(p.order, program.ID)
		count++
	}
	p.mu.Unlock()

	p.app.Log.Debug("programs replaced (networkId=%d, count=%d)", networkID, count)
	p.save()
}

// Save は 1 秒後の emit と 30 秒後の保存を予約する。
func (p *ProgramStore) Save() { p.save() }

func (p *ProgramStore) save() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.emitTimer != nil {
		p.emitTimer.Stop()
	}
	p.emitTimer = p.app.Clock.AfterFunc(time.Second, p.emit)
	if p.saveTimer != nil {
		p.saveTimer.Stop()
	}
	p.saveTimer = p.app.Clock.AfterFunc(30*time.Second, p.persist)
}

func (p *ProgramStore) emit() {
	p.mu.Lock()
	if p.emitRunning {
		p.mu.Unlock()
		return
	}
	p.emitRunning = true
	for len(p.emitOrder) > 0 {
		item := p.emitOrder[0]
		p.emitOrder = p.emitOrder[1:]
		eventType := p.emitType[item]
		delete(p.emitType, item)
		p.mu.Unlock()

		if p.app.Event != nil {
			p.app.Event.Emit(apid.EventResourceProgram, eventType, item)
		}
		time.Sleep(10 * time.Millisecond)

		p.mu.Lock()
	}
	p.emitRunning = false
	p.mu.Unlock()
}

func (p *ProgramStore) persist() {
	p.app.Log.Debug("saving programs...")
	p.mu.Lock()
	values := p.valuesLocked()
	integrity := p.app.Config.ChannelsIntegrity
	path := p.app.Config.ProgramsDBPath
	out := make([]apid.Program, 0, len(values))
	for _, v := range values {
		out = append(out, *v)
	}
	p.mu.Unlock()

	if err := db.SavePrograms(path, out, integrity); err != nil {
		p.app.Log.Error("failed to save programs: %s", err)
	}
}

// Load は programs.json を読み込む。networkId 未設定・放送終了済みは捨てる。
func (p *ProgramStore) Load() error {
	p.app.Log.Debug("loading programs...")

	now := p.app.Clock.Now().UnixMilli()
	dropped := false

	programs, err := db.LoadPrograms(p.app.Config.ProgramsDBPath, p.app.Config.ChannelsIntegrity)
	if err != nil {
		return err
	}
	for i := range programs {
		item := &programs[i]
		if item.NetworkID == 0 {
			dropped = true
			continue
		}
		if now > item.StartAt+item.Duration {
			dropped = true
			continue
		}
		p.Add(item, true)
	}

	if dropped {
		p.save()
	}
	return nil
}

func (p *ProgramStore) findAndRemoveConflictsLocked(added *apid.Program) {
	addedEndAt := added.StartAt + added.Duration

	for _, item := range p.valuesLocked() {
		if item.NetworkID != added.NetworkID || item.ServiceID != added.ServiceID || item.ID == added.ID {
			continue
		}
		itemEndAt := item.StartAt + item.Duration
		if ((added.StartAt <= item.StartAt && item.StartAt < addedEndAt) ||
			(item.StartAt <= added.StartAt && added.StartAt < itemEndAt)) &&
			(!(derefBool(item.IsPresent) || derefBool(item.IsFollowing)) || derefBool(added.IsPresent)) {

			p.itemMapDeleted[item.ID] = item
			delete(p.itemMap, item.ID)
			if p.app.Event != nil {
				p.app.Event.Emit(apid.EventResourceProgram, apid.EventTypeRemove, apid.Program{ID: item.ID})
			}
			p.app.Log.Debug("ProgramItem#%d has removed by overlapped ProgramItem#%d", item.ID, added.ID)
		}
	}
}

// gc は Node 版 Program#_gc の移植。
func (p *ProgramStore) gc() {
	p.app.Log.Debug("Program GC has started")

	now := p.app.Clock.Now().UnixMilli()
	shortExp := now - 3*60*60*1000
	longExp := now - 24*60*60*1000
	maximum := now + 9*24*60*60*1000
	count := 0

	p.mu.Lock()
	for _, item := range p.valuesLocked() {
		exp := shortExp
		if item.Duration == 1 {
			exp = longExp
		}
		if exp > (item.StartAt+item.Duration) || maximum < item.StartAt {
			count++
			delete(p.itemMap, item.ID)
		}
	}
	for id, item := range p.itemMapDeleted {
		exp := shortExp
		if item.Duration == 1 {
			exp = longExp
		}
		if exp > (item.StartAt+item.Duration) || maximum < item.StartAt {
			count++
			delete(p.itemMapDeleted, id)
		}
	}
	p.mu.Unlock()

	p.app.Log.Info("Program GC has finished and removed %d programs", count)
}

func (p *ProgramStore) valuesLocked() []*apid.Program {
	out := make([]*apid.Program, 0, len(p.itemMap))
	for _, id := range p.order {
		if v, ok := p.itemMap[id]; ok {
			out = append(out, v)
		}
	}
	return out
}

// setEmitLocked は emitPrograms に (program, type) を記録する。
// 既に同じ program が積まれていれば type だけ更新し、位置は変えない (Node Map.set と同じ)。
func (p *ProgramStore) setEmitLocked(item *apid.Program, t apid.EventType) {
	if _, ok := p.emitType[item]; ok {
		p.emitType[item] = t
		return
	}
	p.emitType[item] = t
	p.emitOrder = append(p.emitOrder, item)
}

// rejectWhere はクエリ内に $where があれば WhereQueryError を返す。
func rejectWhere(query map[string]string) error {
	for k := range query {
		if k == "$where" {
			return &WhereQueryError{}
		}
	}
	return nil
}

// matchQuery は HTTP 層が実際に使う等値クエリのみを評価する。
// 数値フィールドはクエリ文字列を数値へ変換して比較する (Express の文字列クエリ対応)。
func matchQuery(item *apid.Program, query map[string]string) bool {
	for k, v := range query {
		if v == "" {
			continue
		}
		switch k {
		case "networkId":
			if item.NetworkID != atoi(v) {
				return false
			}
		case "serviceId":
			if item.ServiceID != atoi(v) {
				return false
			}
		case "eventId":
			if item.EventID != atoi(v) {
				return false
			}
		case "name":
			if item.Name == nil || *item.Name != v {
				return false
			}
		default:
			// 未対応キーは無視する (Node 版 sift の演算子は移植対象外)。
		}
	}
	return true
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
