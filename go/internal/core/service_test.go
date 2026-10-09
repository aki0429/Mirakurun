package core

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/db"
)

func TestGetServiceItemID(t *testing.T) {
	if got := GetServiceItemID(32721, 2056); got != 3272102056 {
		t.Errorf("GetServiceItemID=%d", got)
	}
	if got := GetServiceItemID(4, 244); got != 400244 {
		t.Errorf("GetServiceItemID=%d", got)
	}
}

func TestServiceItemGetOrder(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	ch := &ChannelItem{Type: apid.ChannelTypeGR, Channel: "T13", app: env.app}
	it := newServiceItem(env.app, ch, 32721, 2056, "x", apid.Ptr(1), apid.Ptr(8), apid.Ptr(2), false, 0)
	if got := it.GetOrder(); got != 110212056 {
		t.Errorf("GetOrder=%d want 110212056", got)
	}
	// remoteControlKeyId 未設定 → "200"
	it2 := newServiceItem(env.app, ch, 32721, 2056, "x", apid.Ptr(1), apid.Ptr(8), nil, false, 0)
	if got := it2.GetOrder(); got != 120012056 {
		t.Errorf("GetOrder=%d want 120012056", got)
	}
}

func TestServiceItemExportDefault(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	ch := &ChannelItem{Type: apid.ChannelTypeGR, Channel: "T13", app: env.app}
	it := newServiceItem(env.app, ch, 32721, 2056, "x", apid.Ptr(1), apid.Ptr(8), apid.Ptr(2), false, 0)
	e := it.Export()
	if e.ID != 3272102056 || e.ServiceID != 2056 || e.NetworkID != 32721 || e.Name != "x" {
		t.Errorf("export=%+v", e)
	}
	if e.EPGReady == nil || *e.EPGReady != false || e.EPGUpdatedAt == nil || *e.EPGUpdatedAt != 0 {
		t.Errorf("epg fields must be present (false/0): %+v", e)
	}
	if e.Channel == nil || e.Channel.Type != apid.ChannelTypeGR || e.Channel.Channel != "T13" {
		t.Errorf("channel=%+v", e.Channel)
	}
	// JSON must include epgReady:false and epgUpdatedAt:0 (Node 版と同じ)
	b, _ := json.Marshal(e)
	if !contains(string(b), `"epgReady":false`) || !contains(string(b), `"epgUpdatedAt":0`) {
		t.Errorf("json=%s", b)
	}
}

func TestServiceStoreQueries(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	chA := &ChannelItem{Type: apid.ChannelTypeGR, Channel: "T13", app: env.app}
	chB := &ChannelItem{Type: apid.ChannelTypeBS, Channel: "BS1", app: env.app}
	env.app.Service.Add(newServiceItem(env.app, chA, 32721, 2056, "a", apid.Ptr(1), apid.Ptr(8), nil, false, 0))
	env.app.Service.Add(newServiceItem(env.app, chA, 32721, 2057, "b", apid.Ptr(1), apid.Ptr(8), nil, false, 0))
	env.app.Service.Add(newServiceItem(env.app, chB, 4, 101, "c", apid.Ptr(1), apid.Ptr(1), nil, false, 0))

	if len(env.app.Service.FindByChannel(chA)) != 2 {
		t.Errorf("FindByChannel(A)=%d", len(env.app.Service.FindByChannel(chA)))
	}
	if len(env.app.Service.FindByChannel(chB)) != 1 {
		t.Errorf("FindByChannel(B)=%d", len(env.app.Service.FindByChannel(chB)))
	}
	if len(env.app.Service.FindByNetworkID(32721)) != 2 {
		t.Errorf("FindByNetworkID=%d", len(env.app.Service.FindByNetworkID(32721)))
	}
	if len(env.app.Service.FindByNetworkIDWithLogoID(32721, 8)) != 2 {
		t.Errorf("FindByNetworkIDWithLogoID=%d", len(env.app.Service.FindByNetworkIDWithLogoID(32721, 8)))
	}
	if got := env.app.Service.GetByNetworkID(4, 101); got == nil || got.Name() != "c" {
		t.Errorf("GetByNetworkID=%+v", got)
	}
	if !env.app.Service.ExistsByID(3272102056) {
		t.Error("ExistsByID false")
	}
}

func TestServiceItemSetterEmitsUpdate(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	ch := &ChannelItem{Type: apid.ChannelTypeGR, Channel: "T13", app: env.app}
	it := newServiceItem(env.app, ch, 32721, 2056, "x", nil, nil, nil, false, 0)
	env.app.Service.Add(it)

	before := len(env.event.Log())
	it.SetName("y")
	log := env.event.Log()
	if len(log) != before+1 {
		t.Fatalf("expected 1 update event, got %d", len(log)-before)
	}
	ev := log[len(log)-1]
	if ev.Resource != apid.EventResourceService || ev.Type != apid.EventTypeUpdate {
		t.Errorf("event=%+v", ev)
	}
	var data map[string]any
	json.Unmarshal(ev.Data, &data)
	if data["name"] != "y" {
		t.Errorf("event data name=%v", data["name"])
	}

	// 同値設定ではイベントが出ない
	before = len(env.event.Log())
	it.SetName("y")
	if len(env.event.Log()) != before {
		t.Errorf("no-op setter emitted an event")
	}
}

func TestServiceSaveDebounce(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	ch := &ChannelItem{Type: apid.ChannelTypeGR, Channel: "T13", app: env.app}
	env.app.Service.Add(newServiceItem(env.app, ch, 32721, 2056, "debounced", apid.Ptr(1), apid.Ptr(8), nil, false, 0))

	// 10 秒進めると保存される
	env.clock.Advance(10 * time.Second)
	saved, err := db.LoadServices(env.cfg.ServicesDBPath, env.cfg.ChannelsIntegrity)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 || saved[0].Name != "debounced" || saved[0].NetworkID != 32721 {
		t.Fatalf("saved=%+v", saved)
	}
}

func TestServiceLoadMigratesLogoData(t *testing.T) {
	channels := []apid.ConfigChannelsItem{
		{Name: "x", Type: apid.ChannelTypeGR, Channel: "T13"},
	}
	env := newTestEnv(t, channels, &fakeTuner{types: map[apid.ChannelType]bool{apid.ChannelTypeGR: true}})

	logo := base64.StdEncoding.EncodeToString([]byte("PNGDATA"))
	in := []apid.Service{{
		ID: 3272102056, ServiceID: 2056, NetworkID: 32721, Name: "x",
		Type: apid.Ptr(1), LogoID: apid.Ptr(8), LogoData: logo,
		Channel: &apid.ServiceChannel{Type: apid.ChannelTypeGR, Channel: "T13"},
	}}
	if err := db.SaveServices(env.cfg.ServicesDBPath, in, env.cfg.ChannelsIntegrity); err != nil {
		t.Fatal(err)
	}

	if err := env.app.Service.Load(); err != nil {
		t.Fatal(err)
	}
	if !IsLogoDataExists(env.cfg.LogoDataDirPath, 32721, 8) {
		t.Errorf("logo file not written")
	}
	if got := LoadLogoData(env.cfg.LogoDataDirPath, 32721, 8); string(got) != "PNGDATA" {
		t.Errorf("logo data=%q", got)
	}
	// メモリ上の service は logoData を持たない (export に含まれない)
	if env.app.Service.Len() != 1 {
		t.Errorf("services len=%d", env.app.Service.Len())
	}
	b, _ := json.Marshal(env.app.Service.Items()[0].Export())
	if contains(string(b), "logoData") {
		t.Errorf("export must not contain logoData: %s", b)
	}
}
