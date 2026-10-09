package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aki0429/Mirakurun/go/internal/apid"
)

const fixtureRoot = "../../../.hermes-development/fixtures"

// TestChannelsIntegrityMatchesProductionNodeHash is the hard acceptance gate.
// The expected value is the one recorded inside
// .hermes-development/fixtures/db/services.json.
func TestChannelsIntegrityMatchesProductionNodeHash(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{fixtureRoot + "/config/mirakurun/channels.yml", "p8TfT+ZUWr/f9oWU9zQH8h8oRORdW+p9fXLV1RLUZcM="},
		{fixtureRoot + "/config/mirakurun-q1ud/channels.yml", "2vU7J/j9cXs5PmD8fON+PQ4Q8KlJDQ+RQSOBcIca9TA="},
		{"../../../config/channels.yml", "6u6n3WvcmKwQsRmti+8wmRGveTfrqlz0DPRzZ0NAUJM="},
	}
	for _, c := range cases {
		data, err := os.ReadFile(c.path)
		if err != nil {
			t.Fatalf("read %s: %v", c.path, err)
		}
		got, err := ChannelsIntegrity(data)
		if err != nil {
			t.Fatalf("ChannelsIntegrity(%s): %v", c.path, err)
		}
		if got != c.want {
			t.Errorf("integrity(%s) = %q, want %q", c.path, got, c.want)
		} else {
			t.Logf("integrity(%s) = %s (Go == recorded)", c.path, got)
		}
	}
}

// TestIntegrityJSONMatchesNodeGolden proves the generic YAML->JSON walker is
// byte-identical to Node's JSON.stringify(yaml.load(...)). The golden string is
// produced by scratch/core/oracle.js (node_modules/js-yaml + crypto).
func TestIntegrityJSONMatchesNodeGolden(t *testing.T) {
	data, err := os.ReadFile(fixtureRoot + "/config/mirakurun/channels.yml")
	if err != nil {
		t.Fatal(err)
	}
	v, err := parseYAML(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := stringifyJS(v)
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile("testdata/channels-node.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(golden) {
		t.Fatalf("JSON mismatch:\n got len=%d\nwant len=%d", len(got), len(golden))
	}
}

func TestParseJSTypesAndOrder(t *testing.T) {
	yaml := []byte("a: 1\nb: '1'\nc: true\nd: null\ne: hello\nf: 1.5\ng:\n  - x\n  - 2\n")
	v, err := parseYAML(yaml)
	if err != nil {
		t.Fatal(err)
	}
	s, err := stringifyJS(v)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":1,"b":"1","c":true,"d":null,"e":"hello","f":1.5,"g":["x",2]}`
	if s != want {
		t.Fatalf("got %s want %s", s, want)
	}
}

func TestParseYAMLStringEscaping(t *testing.T) {
	yaml := []byte("x: \"a<b>&c\\\"d\\\\e\\ttab\"\n")
	v, err := parseYAML(yaml)
	if err != nil {
		t.Fatal(err)
	}
	s, err := stringifyJS(v)
	if err != nil {
		t.Fatal(err)
	}
	// JSON.stringify does NOT escape <, >, & (Go's json.Marshal would).
	want := `{"x":"a<b>&c\"d\\e\ttab"}`
	if s != want {
		t.Fatalf("got %s want %s", s, want)
	}
}

func TestLoadChannelsTyped(t *testing.T) {
	l := &Loader{
		ChannelsConfigPath: fixtureRoot + "/config/mirakurun/channels.yml",
		DefaultConfigDir:   t.TempDir(),
		Env:                map[string]string{},
	}
	ch, err := l.LoadChannels()
	if err != nil {
		t.Fatal(err)
	}
	if len(ch) != 49 {
		t.Fatalf("len=%d want 49", len(ch))
	}
	first := ch[0]
	if first.Name != "NHKEテレ大阪" || first.Type != apid.ChannelTypeGR || first.Channel != "T13" {
		t.Errorf("first = %+v", first)
	}
	if first.Satellite != " " {
		t.Errorf("deprecated satellite = %q", first.Satellite)
	}
	if got := first.CommandVars["satellite"]; got != " " {
		t.Errorf("commandVars.satellite = %#v", got)
	}
	last := ch[len(ch)-1]
	if last.ServiceID == nil || *last.ServiceID != 43008 {
		t.Errorf("last serviceId = %v", last.ServiceID)
	}
}

func TestLoadServerDefaultsAndValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yml")
	// no allow* lists -> defaults; jobMaxRunning out of range -> dropped.
	os.WriteFile(path, []byte("port: 40772\njobMaxRunning: 200\njobMaxStandby: 0\nallowIPv4CidrRanges:\n  - 10.0.0.0/8\n  - bogus\n"), 0o644)

	l := &Loader{ServerConfigPath: path, DefaultConfigDir: t.TempDir(), Env: map[string]string{}}
	cfg, err := l.LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AllowIPv4CidrRanges == nil || len(cfg.AllowIPv4CidrRanges) != 1 || cfg.AllowIPv4CidrRanges[0] != "10.0.0.0/8" {
		t.Errorf("allowIPv4CidrRanges = %v", cfg.AllowIPv4CidrRanges)
	}
	if cfg.AllowIPv6CidrRanges == nil || cfg.AllowIPv6CidrRanges[0] != "fc00::/7" {
		t.Errorf("allowIPv6CidrRanges = %v", cfg.AllowIPv6CidrRanges)
	}
	if cfg.AllowPNA == nil || !*cfg.AllowPNA {
		t.Errorf("allowPNA = %v", cfg.AllowPNA)
	}
	if cfg.TsplayEndpoint == "" {
		t.Errorf("tsplayEndpoint empty")
	}
	if cfg.JobMaxRunning != nil || cfg.JobMaxStandby != nil {
		t.Errorf("invalid jobMax values should be dropped: %v %v", cfg.JobMaxRunning, cfg.JobMaxStandby)
	}
	if cfg.Hostname == "" {
		t.Errorf("hostname should default to os.Hostname()")
	}
	if cfg.AllowOrigins == nil || len(cfg.AllowOrigins) == 0 {
		t.Errorf("allowOrigins default missing")
	}
}

func TestLoadServerDockerMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yml")
	os.WriteFile(path, []byte("port: 1234\n"), 0o644)
	l := &Loader{ServerConfigPath: path, DefaultConfigDir: t.TempDir(), Env: map[string]string{
		"DOCKER":          "YES",
		"DOCKER_NETWORK":  "bridge",
		"HOSTNAME":        "dockerhost",
		"LOG_LEVEL":       "3",
		"JOB_MAX_RUNNING": "42",
		"ALLOW_PNA":       "false",
		"ALLOW_ORIGINS":   "https://a.example,https://b.example",
		"TSPLAY_ENDPOINT": "https://ts.example/",
		"DISABLE_WEB_UI":  "true",
	}}
	cfg, err := l.LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Path == nil || *cfg.Path != "/var/run/mirakurun.sock" {
		t.Errorf("path = %v", cfg.Path)
	}
	if cfg.Port == nil || *cfg.Port != 40772 {
		t.Errorf("port = %v", cfg.Port)
	}
	if !cfg.DisableIPv6 {
		t.Errorf("disableIPv6 should be true for non-host network")
	}
	if cfg.Hostname != "dockerhost" {
		t.Errorf("hostname = %q", cfg.Hostname)
	}
	if cfg.LogLevel == nil || *cfg.LogLevel != 3 {
		t.Errorf("logLevel = %v", cfg.LogLevel)
	}
	if cfg.JobMaxRunning == nil || *cfg.JobMaxRunning != 42 {
		t.Errorf("jobMaxRunning = %v", cfg.JobMaxRunning)
	}
	if cfg.AllowPNA == nil || *cfg.AllowPNA {
		t.Errorf("allowPNA = %v", cfg.AllowPNA)
	}
	if len(cfg.AllowOrigins) != 2 {
		t.Errorf("allowOrigins = %v", cfg.AllowOrigins)
	}
	if cfg.TsplayEndpoint != "https://ts.example/" {
		t.Errorf("tsplayEndpoint = %q", cfg.TsplayEndpoint)
	}
	if !cfg.DisableWebUI {
		t.Errorf("disableWebUI = %v", cfg.DisableWebUI)
	}
}

func TestLoadChannelsCopiesDefaultWhenMissing(t *testing.T) {
	defDir := t.TempDir()
	os.WriteFile(filepath.Join(defDir, "channels.yml"), []byte("- name: x\n  type: GR\n  channel: T1\n"), 0o644)
	target := filepath.Join(t.TempDir(), "sub", "channels.yml")
	l := &Loader{ChannelsConfigPath: target, DefaultConfigDir: defDir, Env: map[string]string{}}
	ch, err := l.LoadChannels()
	if err != nil {
		t.Fatal(err)
	}
	if len(ch) != 1 || ch[0].Channel != "T1" {
		t.Fatalf("ch = %+v", ch)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target not created: %v", err)
	}
}

func TestSaveAndReloadChannels(t *testing.T) {
	target := filepath.Join(t.TempDir(), "channels.yml")
	l := &Loader{ChannelsConfigPath: target, DefaultConfigDir: t.TempDir(), Env: map[string]string{}}
	in := []apid.ConfigChannelsItem{{Name: "n", Type: apid.ChannelTypeBS, Channel: "BS01_0", CommandVars: map[string]any{"satellite": " --tsid 16400 "}}}
	if err := l.SaveChannels(in); err != nil {
		t.Fatal(err)
	}
	out, err := l.LoadChannels()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Name != "n" || out[0].CommandVars["satellite"] != " --tsid 16400 " {
		t.Fatalf("out = %+v", out)
	}
}
