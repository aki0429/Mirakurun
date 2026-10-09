package job

import (
	"testing"
	"time"
)

func TestIsValidCronExpression(t *testing.T) {
	valid := []string{
		"* * * * *",
		"20,50 * * * *",
		"45 * * * *",
		"5 6 * * *",
		"12 * * * *",
		"*/5 * * * *",
		"1-10/2 0-23 1-31 1-12 0-6",
		"59 23 31 12 6",
		"5-3 * * * *", // 正規表現上は有効 (match 時にエラー)
		"0 0 1 1 1/6",
	}
	for _, s := range valid {
		if !IsValidCronExpression(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	invalid := []string{
		"",
		"* * * *",
		"* * * * * *",
		" * * * * *",
		"*  * * * *",
		"60 * * * *",
		"* 24 * * *",
		"* * 0 * *",
		"* * 32 * *",
		"* * * 0 *",
		"* * * 13 *",
		"* * * * 7",
		"*/0 * * * *",
		"*/60 * * * *",
		"1,,2 * * * *",
		"1, * * * *",
		"a * * * *",
		"00 * * * *",
		"* * * * 0/7",
		"*\t* * * *",
	}
	for _, s := range invalid {
		if IsValidCronExpression(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func at(h, m int, day int) time.Time {
	// 2026-10-04 は日曜日
	return time.Date(2026, 10, day, h, m, 0, 0, time.FixedZone("JST", 9*3600))
}

func TestMatchCronExpression(t *testing.T) {
	cases := []struct {
		expr string
		t    time.Time
		want bool
	}{
		{"20,50 * * * *", at(10, 20, 9), true},
		{"20,50 * * * *", at(10, 50, 9), true},
		{"20,50 * * * *", at(10, 21, 9), false},
		{"45 * * * *", at(3, 45, 9), true},
		{"5 6 * * *", at(6, 5, 9), true},
		{"5 6 * * *", at(7, 5, 9), false},
		{"*/15 * * * *", at(1, 30, 9), true},
		{"*/15 * * * *", at(1, 31, 9), false},
		// Node 版の癖: "5/10" は範囲指定でないので 0 から 10 刻み (5 は無視される)
		{"5/10 * * * *", at(1, 10, 9), true},
		{"5/10 * * * *", at(1, 5, 9), false},
		{"1-10/3 * * * *", at(1, 4, 9), true},
		{"1-10/3 * * * *", at(1, 5, 9), false},
		{"2-4 * * * *", at(1, 3, 9), true},
		{"2-4 * * * *", at(1, 5, 9), false},
		{"* * * * 0", at(1, 5, 4), true}, // 日曜
		{"* * * * 0", at(1, 5, 5), false},
		{"* * 9 10 5", at(1, 5, 9), true}, // 2026-10-09 は金曜
		{"* * 9 11 *", at(1, 5, 9), false},
	}
	for _, c := range cases {
		got, err := MatchCronExpression(c.expr, c.t)
		if err != nil {
			t.Fatalf("%q: %v", c.expr, err)
		}
		if got != c.want {
			t.Errorf("%q at %s = %v want %v", c.expr, c.t, got, c.want)
		}
	}
}

func TestMatchCronExpressionErrors(t *testing.T) {
	for _, expr := range []string{"5-3 * * * *", "* * * *", "61 * * * *", "*/0 * * * *", "10-5/2 * * * *"} {
		if _, err := MatchCronExpression(expr, at(1, 1, 9)); err == nil {
			t.Errorf("%q should error", expr)
		}
	}
}
