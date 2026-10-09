package core

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/event"
	"github.com/aki0429/Mirakurun/go/internal/job"
	"github.com/aki0429/Mirakurun/go/internal/mlog"
)

// fakeTuner は TunerAPI のテスト実装。
type fakeTuner struct {
	types    map[apid.ChannelType]bool
	services []apid.Service
	ready    bool
	epgErr   error
	epgCalls int
	svcCalls int
}

func (f *fakeTuner) TypeExists(t apid.ChannelType) bool { return f.types[t] }

func (f *fakeTuner) ReadyForJob(ctx context.Context, item *ChannelItem) bool { return f.ready }

func (f *fakeTuner) GetServices(ctx context.Context, item *ChannelItem) ([]apid.Service, error) {
	f.svcCalls++
	return f.services, nil
}

func (f *fakeTuner) GetEPG(ctx context.Context, item *ChannelItem, d time.Duration) error {
	f.epgCalls++
	return f.epgErr
}

func quietLogger() *mlog.Logger {
	return mlog.New(mlog.Options{Stdout: io.Discard, Stderr: io.Discard})
}

type testEnv struct {
	app   *App
	cfg   *Config
	tuner *fakeTuner
	event *event.Emitter
	clock *job.FakeClock
}

func newTestEnv(t *testing.T, channels []apid.ConfigChannelsItem, tuner *fakeTuner) *testEnv {
	t.Helper()
	dir := t.TempDir()
	cfg := &Config{
		Server:            &apid.ConfigServer{},
		Channels:          channels,
		ChannelsIntegrity: "test-integrity",
		ServicesDBPath:    filepath.Join(dir, "services.json"),
		ProgramsDBPath:    filepath.Join(dir, "programs.json"),
		LogoDataDirPath:   filepath.Join(dir, "logo"),
	}
	clock := job.NewFakeClock(time.Unix(1_800_000_000, 0))
	ev := event.New(event.Options{Now: func() time.Time { return clock.Now() }})
	app := New(AppOptions{Config: cfg, Tuner: tuner, Clock: clock, Log: quietLogger(), Event: ev, Version: "4.1.5"})
	return &testEnv{app: app, cfg: cfg, tuner: tuner, event: ev, clock: clock}
}
