package core

import (
	"encoding/json"
	"testing"
	"time"
)

func TestStatusCounters(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	s := env.app.Status
	s.AddRPC()
	s.AddRPCN(2)
	s.AddTSFilter(1)
	s.AddDecoder(1)
	s.AddDecoder(-1)
	s.AddError(ErrorUncaughtException)
	s.AddError(ErrorBufferOverflow)
	s.AddError(ErrorBufferOverflow)

	if s.RPCCount() != 3 {
		t.Errorf("rpc=%d", s.RPCCount())
	}
	if s.TSFilterCount() != 1 || s.DecoderCount() != 0 {
		t.Errorf("stream=%d/%d", s.TSFilterCount(), s.DecoderCount())
	}
	if s.ErrorCount(ErrorBufferOverflow) != 2 || s.ErrorCount(ErrorUncaughtException) != 1 || s.ErrorCount(ErrorDecoderRespawn) != 0 {
		t.Errorf("errorCount wrong")
	}
}

func TestStatusEPGGatheringNetworks(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	env.app.Status.SetEPGGathering(32725, true)
	env.app.Status.SetEPGGathering(32721, true)
	env.app.Status.SetEPGGathering(4, false)
	got := env.app.Status.GatheringNetworks()
	if len(got) != 2 || got[0] != 32721 || got[1] != 32725 {
		t.Errorf("gathering=%v (want ascending 32721,32725)", got)
	}
}

func TestTimerAccuracySampler(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	s := env.app.Status
	// 1e9 ns は「ちょうど 1 秒」なので last は 0
	s.recordSampleForTest(1_000_000_000)
	last, m1, m5, m15 := s.TimerAccuracy()
	if last != 0 {
		t.Errorf("last=%d want 0", last)
	}
	if len(m1) != 60 || len(m5) != 300 || len(m15) != 900 {
		t.Errorf("window sizes=%d/%d/%d", len(m1), len(m5), len(m15))
	}
	if m1[59] != 0 {
		t.Errorf("m1 tail=%d", m1[59])
	}

	// 1.5 秒相当 → last = 5e8 ns
	s.recordSampleForTest(1_500_000_000)
	last, m1, _, _ = s.TimerAccuracy()
	if last != 500_000_000 {
		t.Errorf("last=%d want 5e8", last)
	}
	if m1[58] != 0 || m1[59] != 500_000_000 {
		t.Errorf("shift wrong: m1[58]=%d m1[59]=%d", m1[58], m1[59])
	}
}

func TestStartTimerWithFakeClock(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	// 9 秒で基準、10 秒で 1 回目のサンプル → ちょうど 1 秒なので last=0
	env.clock.Advance(time.Second * 10)
	last, _, _, _ := env.app.Status.TimerAccuracy()
	if last != 0 {
		t.Errorf("last=%d want 0 after first sample", last)
	}
	// さらに 1 秒進めると 2 回目のサンプル (last も 0)
	env.clock.Advance(time.Second)
	last, m1, _, _ := env.app.Status.TimerAccuracy()
	if last != 0 {
		t.Errorf("last=%d", last)
	}
	if m1[59] != 0 {
		t.Errorf("m1[59]=%d", m1[59])
	}
}

func TestGetStatusShape(t *testing.T) {
	env := newTestEnv(t, nil, &fakeTuner{})
	env.app.Program.Add(mkProgram(1, 4, 1, 1, 1000, 1000), true)
	env.app.Status.AddRPCN(7)
	env.app.Status.AddTSFilter(1)
	env.app.Status.AddError(ErrorTunerDeviceRespawn)
	env.app.Status.SetEPGGathering(4, true)
	env.app.Status.TunerDeviceCount = func() int { return 2 }

	// timerAccuracy に既知のサンプルを 1 つ入れる
	env.app.Status.recordSampleForTest(2_000_000_000) // last = 1e9 ns = 1e6 μs

	st := env.app.GetStatus()
	if st.Version != "4.1.5" {
		t.Errorf("version=%q", st.Version)
	}
	if st.EPG.StoredEvents != 1 {
		t.Errorf("storedEvents=%d", st.EPG.StoredEvents)
	}
	if len(st.EPG.GatheringNetworks) != 1 || st.EPG.GatheringNetworks[0] != 4 {
		t.Errorf("gatheringNetworks=%v", st.EPG.GatheringNetworks)
	}
	if st.RPCCount != 7 || st.StreamCount.TSFilter != 1 || st.StreamCount.TunerDevice != 2 {
		t.Errorf("counts wrong: %+v", st)
	}
	if st.ErrorCount.TunerDeviceRespawn != 1 {
		t.Errorf("errorCount=%+v", st.ErrorCount)
	}
	if st.TimerAccuracy.Last != 1e6 {
		t.Errorf("timer last=%v want 1e6", st.TimerAccuracy.Last)
	}
	if st.TimerAccuracy.M1.Max != 1e6 {
		t.Errorf("m1.max=%v", st.TimerAccuracy.M1.Max)
	}
	if st.Process.Platform == "" || st.Process.Arch == "" || st.Process.PID == 0 {
		t.Errorf("process=%+v", st.Process)
	}
	// JSON に必須キーが出ること
	b, _ := json.Marshal(st)
	for _, key := range []string{`"time"`, `"version"`, `"process"`, `"epg"`, `"rpcCount"`, `"streamCount"`, `"errorCount"`, `"timerAccuracy"`} {
		if !contains(string(b), key) {
			t.Errorf("status json missing %s", key)
		}
	}
}
