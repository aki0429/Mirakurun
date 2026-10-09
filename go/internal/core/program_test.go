package core

import (
	"testing"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/db"
)

func mkProgram(id int64, networkID, serviceID, eventID int, startAt, duration int64) *apid.Program {
	return &apid.Program{
		ID: id, NetworkID: networkID, ServiceID: serviceID, EventID: eventID,
		StartAt: startAt, Duration: duration, IsFree: false,
	}
}

func countEvents(env *testEnv, resource apid.EventResource, typ apid.EventType) int {
	n := 0
	for _, e := range env.event.Log() {
		if e.Resource == resource && e.Type == typ {
			n++
		}
	}
	return n
}

func TestGetProgramItemID(t *testing.T) {
	if got := GetProgramItemID(4, 244, 53744); got != 40024453744 {
		t.Errorf("GetProgramItemID=%d", got)
	}
	if got := GetProgramItemID(32721, 2056, 53744); got != 327210205653744 {
		t.Errorf("GetProgramItemID=%d", got)
	}
}

func TestProgramAddConflictRemoval(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	env.app.Program.Add(mkProgram(1, 1, 1, 1, 1000, 1000), true)
	// 重複する番組を追加 → 既存が論理削除される
	env.app.Program.Add(mkProgram(2, 1, 1, 2, 1500, 1000), false)

	if env.app.Program.Exists(1) {
		t.Errorf("program 1 should be removed (overlap)")
	}
	if !env.app.Program.IsLogicallyDeleted(1) {
		t.Errorf("program 1 should be logically deleted")
	}
	if !env.app.Program.Exists(2) {
		t.Errorf("program 2 should exist")
	}
	if n := countEvents(env, apid.EventResourceProgram, apid.EventTypeRemove); n != 1 {
		t.Errorf("remove events=%d want 1", n)
	}
}

func TestProgramAddFirstAddSkipsEmit(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	env.app.Program.Add(mkProgram(1, 1, 1, 1, 1000, 1000), true)
	env.clock.Advance(2 * time.Second)
	if n := countEvents(env, apid.EventResourceProgram, apid.EventTypeCreate); n != 0 {
		t.Errorf("firstAdd should not emit create, got %d", n)
	}
}

func TestProgramSetUpdateAndNoOp(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	env.app.Program.Add(mkProgram(1, 1, 1, 1, 1000, 1000), true)

	name := "new name"
	env.app.Program.Set(1, ProgramPatch{Name: &name})
	env.clock.Advance(time.Second)
	if n := countEvents(env, apid.EventResourceProgram, apid.EventTypeUpdate); n != 1 {
		t.Fatalf("update events=%d want 1", n)
	}
	if got := *env.app.Program.Get(1).Name; got != "new name" {
		t.Errorf("name=%q", got)
	}

	// 同値では emit されない
	env.app.Program.Set(1, ProgramPatch{Name: &name})
	env.clock.Advance(time.Second)
	if n := countEvents(env, apid.EventResourceProgram, apid.EventTypeUpdate); n != 1 {
		t.Errorf("no-op set should not emit, update events=%d", n)
	}
}

func TestProgramSetExtendedMergeAndVideo(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	env.app.Program.Add(mkProgram(1, 1, 1, 1, 1000, 1000), true)

	env.app.Program.Set(1, ProgramPatch{Extended: map[string]string{"a": "1", "b": "2"}})
	env.app.Program.Set(1, ProgramPatch{Extended: map[string]string{"b": "2", "c": "3"}})
	pr := env.app.Program.Get(1)
	if pr.Extended == nil || pr.Extended.Len() != 3 {
		t.Fatalf("extended=%v", pr.Extended)
	}
	if v, _ := pr.Extended.Get("a"); v != "1" {
		t.Errorf("a=%q", v)
	}
	if v, _ := pr.Extended.Get("c"); v != "3" {
		t.Errorf("c=%q", v)
	}

	tp := "mpeg2"
	res := "1080i"
	env.app.Program.Set(1, ProgramPatch{Video: &apid.ProgramVideo{Type: &tp, Resolution: &res, StreamContent: 1, ComponentType: 179}})
	if pr.Video == nil || *pr.Video.Type != "mpeg2" || *pr.Video.Resolution != "1080i" {
		t.Fatalf("video=%+v", pr.Video)
	}
}

func TestProgramSetRecoversLogicallyDeleted(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	env.app.Program.Add(mkProgram(1, 1, 1, 1, 1000, 1000), true)
	env.app.Program.Remove(1, true)
	if env.app.Program.Exists(1) || !env.app.Program.IsLogicallyDeleted(1) {
		t.Fatal("logical delete failed")
	}
	name := "recovered"
	env.app.Program.Set(1, ProgramPatch{Name: &name})
	if !env.app.Program.Exists(1) || env.app.Program.IsLogicallyDeleted(1) {
		t.Errorf("program should be recovered")
	}
}

func TestProgramRemoveHard(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	env.app.Program.Add(mkProgram(1, 1, 1, 1, 1000, 1000), true)
	env.app.Program.Remove(1, false)
	if env.app.Program.Exists(1) || env.app.Program.IsLogicallyDeleted(1) {
		t.Errorf("hard remove failed")
	}
}

func TestProgramFindByQuery(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	env.app.Program.Add(mkProgram(1, 4, 1, 1, 1000, 1000), true)
	env.app.Program.Add(mkProgram(2, 4, 2, 2, 1000, 1000), true)
	env.app.Program.Add(mkProgram(3, 5, 1, 1, 1000, 1000), true)

	got, err := env.app.Program.FindByQuery(map[string]string{"networkId": "4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("networkId=4 => %d, want 2", len(got))
	}
	got, err = env.app.Program.FindByQuery(map[string]string{"networkId": "4", "serviceId": "1"})
	if err != nil || len(got) != 1 || got[0].ID != 1 {
		t.Errorf("composite query=%v err=%v", got, err)
	}
	got, _ = env.app.Program.FindByQuery(map[string]string{})
	if len(got) != 3 {
		t.Errorf("empty query=%d want 3", len(got))
	}
}

func TestProgramFindByQueryRejectsWhere(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	_, err := env.app.Program.FindByQuery(map[string]string{"$where": "true"})
	if err == nil {
		t.Fatal("expected WhereQueryError")
	}
	if _, ok := err.(*WhereQueryError); !ok {
		t.Errorf("err type=%T", err)
	}
}

func TestProgramFindByNetworkIDAndTime(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	env.app.Program.Add(mkProgram(1, 4, 1, 1, 1000, 1000), true) // 1000..2000
	env.app.Program.Add(mkProgram(2, 4, 1, 2, 3000, 1000), true) // 3000..4000
	got := env.app.Program.FindByNetworkIDAndTime(4, 1500)
	if len(got) != 1 || got[0].ID != 1 {
		t.Errorf("at 1500=%v", got)
	}
	if got := env.app.Program.FindByNetworkIDAndTime(4, 2500); len(got) != 0 {
		t.Errorf("at 2500=%v", got)
	}
}

func TestProgramFindByNetworkIDAndReplace(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	env.app.Program.Add(mkProgram(1, 4, 1, 1, 1000, 1000), true)
	env.app.Program.Add(mkProgram(2, 5, 1, 1, 1000, 1000), true)
	env.app.Program.FindByNetworkIDAndReplace(4, []*apid.Program{mkProgram(3, 4, 1, 3, 5000, 1000)})
	if env.app.Program.Exists(1) {
		t.Errorf("old netId=4 program should be gone")
	}
	if !env.app.Program.Exists(3) || !env.app.Program.Exists(2) {
		t.Errorf("replace result wrong")
	}
}

func TestProgramGC(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	now := env.clock.Now().UnixMilli()
	env.app.Program.Add(mkProgram(1, 1, 1, 1, now-4*60*60*1000, 60*1000), true)     // short exp → removed
	env.app.Program.Add(mkProgram(2, 1, 1, 2, now-4*60*60*1000, 1), true)           // duration=1 → long exp → kept
	env.app.Program.Add(mkProgram(3, 1, 1, 3, now+10*24*60*60*1000, 60*1000), true) // too far → removed
	env.app.Program.Add(mkProgram(4, 1, 1, 4, now, 60*1000), true)                  // current → kept

	env.app.Program.gc()
	if env.app.Program.Exists(1) {
		t.Errorf("expired short program should be GC'd")
	}
	if !env.app.Program.Exists(2) {
		t.Errorf("duration=1 program should survive short exp")
	}
	if env.app.Program.Exists(3) {
		t.Errorf("too-far program should be GC'd")
	}
	if !env.app.Program.Exists(4) {
		t.Errorf("current program should survive")
	}
}

func TestProgramLoadDropsExpiredAndMissingNetworkID(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	now := env.clock.Now().UnixMilli()
	items := []apid.Program{
		*mkProgram(1, 4, 1, 1, now-100000, 1000),   // expired
		*mkProgram(2, 4, 1, 2, now+100000, 100000), // valid
		*mkProgram(3, 0, 1, 3, now+100000, 100000), // missing networkId (0)
	}
	if err := db.SavePrograms(env.cfg.ProgramsDBPath, items, env.cfg.ChannelsIntegrity); err != nil {
		t.Fatal(err)
	}
	if err := env.app.Program.Load(); err != nil {
		t.Fatal(err)
	}
	if env.app.Program.Len() != 1 || !env.app.Program.Exists(2) {
		t.Errorf("loaded=%d exists2=%v", env.app.Program.Len(), env.app.Program.Exists(2))
	}
}

func TestProgramGCScheduleRegistered(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	for _, s := range env.app.Job.Schedules() {
		if s.Key == "Program.GC" && s.Schedule != "45 * * * *" {
			t.Errorf("Program.GC schedule=%q", s.Schedule)
		}
	}
}
