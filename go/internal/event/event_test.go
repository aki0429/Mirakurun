package event

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
)

func newTestEmitter() *Emitter {
	return New(Options{Now: func() time.Time { return time.UnixMilli(1791400000123) }})
}

func TestEmitSnapshotsDataAsJSONAtEmitTime(t *testing.T) {
	e := newTestEmitter()
	data := map[string]any{"id": 1, "name": "a"}
	e.Emit(apid.EventResourceService, apid.EventTypeCreate, data)
	data["name"] = "mutated" // deepClone 相当: 後からの変更は反映されない

	log := e.Log()
	if len(log) != 1 {
		t.Fatalf("log len %d", len(log))
	}
	got, _ := json.Marshal(log[0])
	want := `{"resource":"service","type":"create","data":{"id":1,"name":"a"},"time":1791400000123}`
	if string(got) != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestLogKeepsLast100(t *testing.T) {
	e := newTestEmitter()
	for i := 0; i < 105; i++ {
		e.Emit(apid.EventResourceProgram, apid.EventTypeUpdate, i)
	}
	log := e.Log()
	if len(log) != 100 || string(log[0].Data) != "5" || string(log[99].Data) != "104" {
		t.Fatalf("len=%d first=%s last=%s", len(log), log[0].Data, log[len(log)-1].Data)
	}
}

func TestSubscribersReceiveEventsInOrderAndCanUnsubscribe(t *testing.T) {
	e := newTestEmitter()
	ch, cancel := e.OnEvent(10)
	e.Emit(apid.EventResourceJob, apid.EventTypeCreate, "a")
	e.Emit(apid.EventResourceJob, apid.EventTypeUpdate, "b")
	if ev := <-ch; ev.Type != apid.EventTypeCreate || string(ev.Data) != `"a"` {
		t.Fatalf("first %+v", ev)
	}
	if ev := <-ch; ev.Type != apid.EventTypeUpdate {
		t.Fatalf("second %+v", ev)
	}
	cancel()
	cancel()
	e.Emit(apid.EventResourceJob, apid.EventTypeRemove, "c")
	if _, ok := <-ch; ok {
		t.Fatal("expected closed channel")
	}
}

func TestSlowSubscriberDoesNotBlockEmitter(t *testing.T) {
	e := newTestEmitter()
	_, cancel := e.OnEvent(2)
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			e.Emit(apid.EventResourceTuner, apid.EventTypeUpdate, i)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("emitter blocked")
	}
	if e.Dropped() != 8 {
		t.Fatalf("dropped %d", e.Dropped())
	}
}

func TestRawMessageDataIsNotDoubleEncoded(t *testing.T) {
	e := newTestEmitter()
	e.Emit(apid.EventResourceProgram, apid.EventTypeRemove, json.RawMessage(`{"id":1}`))
	if got := string(e.Log()[0].Data); got != `{"id":1}` {
		t.Fatalf("got %s", got)
	}
}

func TestConcurrentEmitIsSafe(t *testing.T) {
	e := newTestEmitter()
	ch, cancel := e.OnEvent(10000)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				e.Emit(apid.EventResourceProgram, apid.EventTypeUpdate, j)
				_ = e.Log()
			}
		}()
	}
	wg.Wait()
	if len(ch) != 800 || len(e.Log()) != 100 {
		t.Fatalf("delivered %d log %d", len(ch), len(e.Log()))
	}
}
