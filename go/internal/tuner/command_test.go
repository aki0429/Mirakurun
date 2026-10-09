package tuner

import "testing"

// TestReplaceCommandTemplate は Node 版 common.replaceCommandTemplate と同じ置換を確認する。
func TestReplaceCommandTemplate(t *testing.T) {
	vars := map[string]any{
		"channel":   "T13",
		"type":      "GR",
		"satellite": " ",
		"space":     0,
		"freq":      12345,
	}
	cases := []struct{ in, want string }{
		{"recisdb tune --channel <channel> -", "recisdb tune --channel T13 -"},
		{"cmd <channel><satellite>-", "cmd T13 -"},
		{"cmd <type> <space> <freq>", "cmd GR 0 12345"},
		// 未知キーは空文字列へ、テンプレートでない < > も空になる (Node と同じ)
		{"a <unknown> b", "a  b"},
		{"<CHANNEL>", "T13"}, // 大文字小文字を区別しない
	}
	for _, c := range cases {
		if got := ReplaceCommandTemplate(c.in, vars); got != c.want {
			t.Errorf("ReplaceCommandTemplate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestReplaceCommandTemplateBoolAndFloat は JS String() と同じ数値/真偽の文字列化を確認する。
func TestReplaceCommandTemplateBoolAndFloat(t *testing.T) {
	vars := map[string]any{"a": true, "b": float64(1000000), "c": 3.5}
	if got := ReplaceCommandTemplate("<a> <b> <c>", vars); got != "true 1000000 3.5" {
		t.Errorf("got %q", got)
	}
}

// TestParseCommandForSpawn は Node 版 parseCommandForSpawn と同じ分割を確認する。
func TestParseCommandForSpawn(t *testing.T) {
	cases := []struct {
		in      string
		command string
		args    []string
	}{
		{"bash -c \"cat /x/y.ts\"", "bash", []string{"-c", "cat /x/y.ts"}},
		{"recisdb tune --device /dev/px4video2 --channel T13 -", "recisdb", []string{"tune", "--device", "/dev/px4video2", "--channel", "T13", "-"}},
		{"'/opt/my prog' a b", "/opt/my prog", []string{"a", "b"}},
		{"cmd", "cmd", nil},
	}
	for _, c := range cases {
		cmd, args, err := ParseCommandForSpawn(c.in)
		if err != nil {
			t.Fatalf("ParseCommandForSpawn(%q) error: %v", c.in, err)
		}
		if cmd != c.command {
			t.Errorf("ParseCommandForSpawn(%q) command = %q, want %q", c.in, cmd, c.command)
		}
		if len(args) != len(c.args) {
			t.Errorf("ParseCommandForSpawn(%q) args = %#v, want %#v", c.in, args, c.args)
			continue
		}
		for i := range args {
			if args[i] != c.args[i] {
				t.Errorf("ParseCommandForSpawn(%q) args[%d] = %q, want %q", c.in, i, args[i], c.args[i])
			}
		}
	}
	if _, _, err := ParseCommandForSpawn("   "); err == nil {
		t.Errorf("空コマンドはエラーになるべき")
	}
}
