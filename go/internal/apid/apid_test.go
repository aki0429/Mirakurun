package apid

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestOrderedStringMapKeepsInsertionOrder(t *testing.T) {
	m := &OrderedStringMap{}
	m.Set("番組内容", "a")
	m.Set("出演者", "b")
	m.Set("番組内容", "c") // 上書きは順序を保つ
	got, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"番組内容":"c","出演者":"b"}`
	if string(got) != want {
		t.Fatalf("got %s want %s", got, want)
	}

	var back OrderedStringMap
	if err := json.Unmarshal([]byte(`{"z":"1","a":"2","m":"3"}`), &back); err != nil {
		t.Fatal(err)
	}
	if k := back.Keys(); len(k) != 3 || k[0] != "z" || k[1] != "a" || k[2] != "m" {
		t.Fatalf("keys %v", k)
	}
	out, _ := json.Marshal(&back)
	if !bytes.Equal(out, []byte(`{"z":"1","a":"2","m":"3"}`)) {
		t.Fatalf("roundtrip %s", out)
	}
}

func TestProgramJSONOmitsUndefinedFields(t *testing.T) {
	p := Program{ID: 1, EventID: 2, ServiceID: 3, NetworkID: 4, StartAt: 5, Duration: 6, IsFree: true}
	got, _ := json.Marshal(p)
	want := `{"id":1,"eventId":2,"serviceId":3,"networkId":4,"startAt":5,"duration":6,"isFree":true}`
	if string(got) != want {
		t.Fatalf("got %s", got)
	}
}
