package core

import (
	"context"
	"testing"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/job"
)

func TestChannelLoadValidation(t *testing.T) {
	channels := []apid.ConfigChannelsItem{
		{Name: "a", Type: apid.ChannelTypeGR, Channel: "T1"},
		{Name: "b", Type: apid.ChannelTypeGR, Channel: "T1"},                   // dedupe
		{Name: "c", Type: "XX", Channel: "T2"},                                 // invalid type
		{Name: "", Type: apid.ChannelTypeGR, Channel: "T3"},                    // invalid name
		{Name: "d", Type: apid.ChannelTypeGR, Channel: "T4", IsDisabled: true}, // disabled
		{Name: "e", Type: apid.ChannelTypeCS, Channel: "C1"},                   // no tuner type
		{Name: "f", Type: apid.ChannelTypeBS, Channel: "BS1", ServiceID: apid.Ptr(151), Satelite: " JCSAT3A "},
	}
	env := newTestEnv(t, channels, &fakeTuner{types: map[apid.ChannelType]bool{apid.ChannelTypeGR: true, apid.ChannelTypeBS: true}})

	items := env.app.Channel.Items()
	if len(items) != 2 {
		t.Fatalf("len=%d want 2: %+v", len(items), items)
	}
	if items[0].Name != "a" || items[0].Channel != "T1" {
		t.Errorf("items[0]=%+v", items[0])
	}
	// serviceId 付きは name が "type:channel" に書き換わる
	if items[1].Name != "BS:BS1" || items[1].Type != apid.ChannelTypeBS {
		t.Errorf("items[1]=%+v", items[1])
	}
	// deprecated satelite が commandVars.satellite に移行される
	if got := items[1].CommandVars["satellite"]; got != " JCSAT3A " {
		t.Errorf("commandVars.satellite=%#v", got)
	}
	// config 側も移行済み (initJobs が config を走査するため)
	if channels[6].CommandVars["satellite"] != " JCSAT3A " {
		t.Errorf("config not migrated: %#v", channels[6].CommandVars)
	}
}

func TestChannelDeprecatedCommandVarsMigration(t *testing.T) {
	channels := []apid.ConfigChannelsItem{
		{Name: "x", Type: apid.ChannelTypeBS, Channel: "BS9", Satellite: "s", Space: apid.Ptr(6), Freq: apid.Ptr(12345), Polarity: "H"},
	}
	env := newTestEnv(t, channels, &fakeTuner{types: map[apid.ChannelType]bool{apid.ChannelTypeBS: true}})
	it := env.app.Channel.Items()[0]
	for k, want := range map[string]any{"satellite": "s", "space": 6, "freq": 12345, "polarity": "H"} {
		if it.CommandVars[k] != want {
			t.Errorf("commandVars[%s]=%#v want %#v", k, it.CommandVars[k], want)
		}
	}
}

func TestChannelInvalidCommandVarDropped(t *testing.T) {
	channels := []apid.ConfigChannelsItem{
		{Name: "x", Type: apid.ChannelTypeGR, Channel: "T1", CommandVars: map[string]any{"good": "v", "num": 3, "bad": true}},
	}
	env := newTestEnv(t, channels, &fakeTuner{types: map[apid.ChannelType]bool{apid.ChannelTypeGR: true}})
	cv := env.app.Channel.Items()[0].CommandVars
	if _, ok := cv["bad"]; ok {
		t.Errorf("bool commandVar should be dropped: %#v", cv)
	}
	if cv["good"] != "v" || cv["num"] != 3 {
		t.Errorf("commandVars=%#v", cv)
	}
}

func TestChannelEPGGathererSkipsAndConsidersBroadcastOff(t *testing.T) {
	channels := []apid.ConfigChannelsItem{
		{Name: "x", Type: apid.ChannelTypeGR, Channel: "T1", ServiceID: apid.Ptr(1)},
	}
	env := newTestEnv(t, channels, &fakeTuner{types: map[apid.ChannelType]bool{apid.ChannelTypeGR: true}, ready: true})

	// サービスを 1 件用意 (networkId=32721)
	ch := env.app.Channel.Items()[0]
	svc := newServiceItem(env.app, ch, 32721, 1, "svc", apid.Ptr(1), apid.Ptr(0), apid.Ptr(1), true, env.clock.Now().UnixMilli())
	env.app.Service.Add(svc)

	// EPG 回収中なら readyFn は false
	env.app.Status.SetEPGGathering(32721, true)
	if err := env.app.Channel.epgGatherer(context.Background()); err != nil {
		t.Fatal(err)
	}
	jobs := env.app.Job.Jobs()
	var nidJob *apid.JobItem
	for i := range jobs {
		if jobs[i].Key == "EPG.Gather.NID.32721" {
			nidJob = &jobs[i]
		}
	}
	if nidJob == nil {
		t.Fatal("EPG.Gather.NID.32721 not added")
	}
	if nidJob.IsRerunnable == nil || !*nidJob.IsRerunnable {
		t.Errorf("isRerunnable should be true")
	}
}

func TestChannelEPGJobAndScheduleRegistered(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{types: map[apid.ChannelType]bool{}})
	var found bool
	for _, s := range env.app.Job.Schedules() {
		if s.Key == "EPG.Gatherer" {
			found = true
			if s.Schedule != "20,50 * * * *" {
				t.Errorf("schedule=%q", s.Schedule)
			}
		}
	}
	if !found {
		t.Error("EPG.Gatherer schedule not registered")
	}
}

func TestChannelEPGDisabledWhenDisableEITParsing(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Server:            &apid.ConfigServer{DisableEITParsing: true},
		ChannelsIntegrity: "i",
		ServicesDBPath:    dir + "/s.json",
		ProgramsDBPath:    dir + "/p.json",
		LogoDataDirPath:   dir + "/logo",
	}
	clock := job.NewFakeClock(time.Unix(1_800_000_000, 0))
	app := New(AppOptions{Config: cfg, Tuner: &fakeTuner{}, Clock: clock, Log: quietLogger()})
	for _, s := range app.Job.Schedules() {
		if s.Key == "EPG.Gatherer" {
			t.Error("EPG.Gatherer should not be registered when disableEITParsing")
		}
	}
}
