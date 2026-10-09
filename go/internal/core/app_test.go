package core

import (
	"testing"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/job"
)

func TestAppJobSchedulesDefault(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	schedules := env.app.Job.Schedules()
	want := map[string]string{"EPG.Gatherer": "20,50 * * * *", "Program.GC": "45 * * * *"}
	got := map[string]string{}
	for _, s := range schedules {
		got[s.Key] = s.Schedule
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("schedule %s=%q want %q", k, got[k], v)
		}
	}
}

func TestAppJobSchedulesFromConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Server:            &apid.ConfigServer{EPGGatheringJobSchedule: "12 * * * *", ProgramGCJobSchedule: "30 * * * *"},
		ChannelsIntegrity: "i",
		ServicesDBPath:    dir + "/s.json",
		ProgramsDBPath:    dir + "/p.json",
		LogoDataDirPath:   dir + "/logo",
	}
	clock := job.NewFakeClock(time.Unix(1_800_000_000, 0))
	app := New(AppOptions{Config: cfg, Tuner: &fakeTuner{}, Clock: clock, Log: quietLogger()})
	got := map[string]string{}
	for _, s := range app.Job.Schedules() {
		got[s.Key] = s.Schedule
	}
	if got["EPG.Gatherer"] != "12 * * * *" || got["Program.GC"] != "30 * * * *" {
		t.Errorf("config schedules not honored: %v", got)
	}
}

func TestServiceInitJobsRetryParams(t *testing.T) {
	channels := []apid.ConfigChannelsItem{
		{Name: "x", Type: apid.ChannelTypeGR, Channel: "T1", ServiceID: apid.Ptr(1)},
	}
	env := newTestEnv(t, channels, &fakeTuner{types: map[apid.ChannelType]bool{apid.ChannelTypeGR: true}})
	env.app.Service.initJobs()

	var check *apid.JobItem
	for i, j := range env.app.Job.Jobs() {
		if j.Key == "Service.Add.Check.GR.T1.1" {
			check = &env.app.Job.Jobs()[i]
		}
	}
	if check == nil {
		t.Fatalf("Service.Add.Check job not queued: %+v", env.app.Job.Jobs())
	}
	if check.RetryOnFail == nil || !*check.RetryOnFail {
		t.Errorf("retryOnFail not set")
	}
	if check.RetryMax == nil || *check.RetryMax != 240 {
		t.Errorf("retryMax=%v want 240", check.RetryMax)
	}
	if check.RetryDelay == nil || *check.RetryDelay != 180000 {
		t.Errorf("retryDelay=%v want 180000", check.RetryDelay)
	}

	// Service.Updater.Add-Schedule が登録される
	found := false
	for _, s := range env.app.Job.Schedules() {
		if s.Key == "Service.Updater.Add-Schedule" {
			found = true
			if s.Schedule != "5 6 * * *" {
				t.Errorf("updater schedule=%q", s.Schedule)
			}
		}
	}
	if !found {
		t.Errorf("Service.Updater.Add-Schedule schedule not registered")
	}
}

func TestServiceInitJobsCalledAfter10s(t *testing.T) {
	channels := []apid.ConfigChannelsItem{
		{Name: "x", Type: apid.ChannelTypeGR, Channel: "T1", ServiceID: apid.Ptr(1)},
	}
	env := newTestEnv(t, channels, &fakeTuner{types: map[apid.ChannelType]bool{apid.ChannelTypeGR: true}})
	if err := env.app.Service.Load(); err != nil {
		t.Fatal(err)
	}
	// 10 秒前は未登録
	for _, s := range env.app.Job.Schedules() {
		if s.Key == "Service.Updater.Add-Schedule" {
			t.Fatal("updater schedule should not be registered before 10s")
		}
	}
	env.clock.Advance(10 * time.Second)
	found := false
	for _, s := range env.app.Job.Schedules() {
		if s.Key == "Service.Updater.Add-Schedule" {
			found = true
		}
	}
	if !found {
		t.Errorf("updater schedule should be registered after 10s")
	}
}

func TestAppGetStatusCountsTunerDevices(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	env.app.Status.TunerDeviceCount = func() int { return 3 }
	st := env.app.GetStatus()
	if st.StreamCount.TunerDevice != 3 {
		t.Errorf("tunerDevice=%d", st.StreamCount.TunerDevice)
	}
}
