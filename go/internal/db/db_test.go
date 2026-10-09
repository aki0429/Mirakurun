package db

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aki0429/Mirakurun/go/internal/apid"
	"github.com/aki0429/Mirakurun/go/internal/mlog"
)

const (
	fixtureDB     = "../../../.hermes-development/fixtures/db"
	prodIntegrity = "p8TfT+ZUWr/f9oWU9zQH8h8oRORdW+p9fXLV1RLUZcM="
	q1udIntegrity = "2vU7J/j9cXs5PmD8fON+PQ4Q8KlJDQ+RQSOBcIca9TA="
	fixtureDBQ1UD = "../../../.hermes-development/fixtures/db-q1ud"
)

// newBufferLogger は default ロガーを error/warn を捕捉できるロガーへ差し替える。
func newBufferLogger() (*mlog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	l := mlog.New(mlog.Options{Stdout: buf, Stderr: buf})
	mlog.SetDefault(l)
	return l, buf
}

func TestLoadServicesFixture(t *testing.T) {
	services, err := LoadServices(filepath.Join(fixtureDB, "services.json"), prodIntegrity)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 141 {
		t.Fatalf("len=%d want 141", len(services))
	}
	s := services[0]
	if s.ID != 3272102056 || s.ServiceID != 2056 || s.NetworkID != 32721 || s.Name != "ＮＨＫＥテレ１大阪" {
		t.Errorf("first = %+v", s)
	}
	if s.Channel == nil || s.Channel.Type != apid.ChannelTypeGR || s.Channel.Channel != "T13" {
		t.Errorf("channel = %+v", s.Channel)
	}
}

func TestLoadServicesFixtureQ1UD(t *testing.T) {
	services, err := LoadServices(filepath.Join(fixtureDBQ1UD, "services.json"), q1udIntegrity)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 24 {
		t.Fatalf("len=%d want 24", len(services))
	}
}

func TestLoadProgramsFixture(t *testing.T) {
	start := time.Now()
	programs, err := LoadPrograms(filepath.Join(fixtureDB, "programs.json"), prodIntegrity)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	t.Logf("loaded %d programs in %s", len(programs), elapsed)
	if len(programs) != 27464 {
		t.Fatalf("len=%d want 27464", len(programs))
	}
	p := programs[0]
	if p.NetworkID != 4 || p.EventID != 53744 {
		t.Errorf("first = %+v", p)
	}
}

func TestIntegrityMismatchReturnsEmptyWithWarn(t *testing.T) {
	_, buf := newBufferLogger()
	services, err := LoadServices(filepath.Join(fixtureDB, "services.json"), "wrong-integrity")
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 0 {
		t.Fatalf("len=%d want 0", len(services))
	}
	if !strings.Contains(buf.String(), "integrity check has failed") {
		t.Errorf("expected warn log, got %q", buf.String())
	}
}

func TestBrokenJSONReturnsEmptyWithError(t *testing.T) {
	_, buf := newBufferLogger()
	path := filepath.Join(t.TempDir(), "services.json")
	os.WriteFile(path, []byte("[{not json"), 0o644)
	services, err := LoadServices(path, prodIntegrity)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 0 {
		t.Fatalf("len=%d want 0", len(services))
	}
	if !strings.Contains(buf.String(), "is broken") {
		t.Errorf("expected error log, got %q", buf.String())
	}
}

func TestLoadMissingFileReturnsEmpty(t *testing.T) {
	services, err := LoadServices(filepath.Join(t.TempDir(), "nope.json"), prodIntegrity)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 0 {
		t.Fatalf("len=%d want 0", len(services))
	}
}

func TestNoIntegrityHeaderReturnsArrayAsIs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "services.json")
	os.WriteFile(path, []byte(`[{"id":1,"serviceId":2,"networkId":3,"name":"x"}]`), 0o644)
	services, err := LoadServices(path, "whatever")
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || services[0].ID != 1 {
		t.Fatalf("services = %+v", services)
	}
}

func TestSaveHeaderProtocolAndAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "services.json")
	in := []apid.Service{{ID: 1, ServiceID: 2, NetworkID: 3, Name: "x"}}
	if err := SaveServices(path, in, prodIntegrity); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), `[{"__integrity__":"`+prodIntegrity+`"},`) {
		t.Fatalf("header wrong: %.80s", raw)
	}
	if strings.HasSuffix(string(raw), "\n") {
		t.Errorf("unexpected trailing newline")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf(".tmp file left behind")
	}
	// reload via protocol
	out, err := LoadServices(path, prodIntegrity)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != 1 {
		t.Fatalf("out = %+v", out)
	}
	// mismatch -> empty
	out2, _ := LoadServices(path, "other")
	if len(out2) != 0 {
		t.Fatalf("mismatch should be empty, got %d", len(out2))
	}
}

func TestRoundTripFixtures(t *testing.T) {
	newBufferLogger()
	dir := t.TempDir()

	// services
	sStart := time.Now()
	services, err := LoadServices(filepath.Join(fixtureDB, "services.json"), prodIntegrity)
	if err != nil {
		t.Fatal(err)
	}
	sPath := filepath.Join(dir, "services.json")
	if err := SaveServices(sPath, services, prodIntegrity); err != nil {
		t.Fatal(err)
	}
	services2, err := LoadServices(sPath, prodIntegrity)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(services, services2) {
		t.Fatalf("services round-trip mismatch (%d vs %d)", len(services), len(services2))
	}
	t.Logf("services round-trip %d items in %s", len(services), time.Since(sStart))

	// programs
	pStart := time.Now()
	programs, err := LoadPrograms(filepath.Join(fixtureDB, "programs.json"), prodIntegrity)
	if err != nil {
		t.Fatal(err)
	}
	pPath := filepath.Join(dir, "programs.json")
	if err := SavePrograms(pPath, programs, prodIntegrity); err != nil {
		t.Fatal(err)
	}
	programs2, err := LoadPrograms(pPath, prodIntegrity)
	if err != nil {
		t.Fatal(err)
	}
	if len(programs) != len(programs2) {
		t.Fatalf("programs len mismatch %d vs %d", len(programs), len(programs2))
	}
	if !reflect.DeepEqual(programs, programs2) {
		for i := range programs {
			if !reflect.DeepEqual(programs[i], programs2[i]) {
				t.Fatalf("program[%d] mismatch:\n%+v\n%+v", i, programs[i], programs2[i])
			}
		}
		t.Fatalf("programs round-trip mismatch")
	}
	t.Logf("programs round-trip %d items in %s", len(programs), time.Since(pStart))
}
