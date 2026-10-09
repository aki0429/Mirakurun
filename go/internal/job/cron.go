package job

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// cronPatterns は Node 版 isValidCronExpression の正規表現 (分, 時, 日, 月, 曜日)。
var cronPatterns = [5]*regexp.Regexp{
	regexp.MustCompile(`^(\*|([0-9]|[1-5][0-9])((-([0-9]|[1-5][0-9]))?))(\/([1-9]|[1-5][0-9]))?$`),
	regexp.MustCompile(`^(\*|([0-9]|1[0-9]|2[0-3])((-([0-9]|1[0-9]|2[0-3]))?))(\/([1-9]|1[0-9]|2[0-3]))?$`),
	regexp.MustCompile(`^(\*|([1-9]|[12][0-9]|3[01])((-([1-9]|[12][0-9]|3[01]))?))(\/([1-9]|[12][0-9]|3[01]))?$`),
	regexp.MustCompile(`^(\*|([1-9]|1[0-2])((-([1-9]|1[0-2]))?))(\/([1-9]|1[0-2]))?$`),
	regexp.MustCompile(`^(\*|([0-6])((-([0-6]))?))(\/([1-6]))?$`),
}

// IsValidCronExpression は Node 版 isValidCronExpression と同じ判定を行う。
// 書式は `m h dom mon dow` (半角スペース 1 個区切り, カンマ区切りの列挙可)。
func IsValidCronExpression(expr string) bool {
	parts := strings.Split(expr, " ")
	if len(parts) != 5 {
		return false
	}
	for i, p := range parts {
		for _, v := range strings.Split(p, ",") {
			if v == "" || !cronPatterns[i].MatchString(v) {
				return false
			}
		}
	}
	return true
}

// MatchCronExpression は Node 版 matchCronExpression の移植。
// t のローカル時刻 (t.Location()) の分・時・日・月・曜日で照合する。
func MatchCronExpression(expr string, t time.Time) (bool, error) {
	parts := strings.Split(expr, " ")
	if len(parts) != 5 {
		return false, fmt.Errorf("Invalid schedule format: %s", expr)
	}
	type field struct {
		value, min, max int
	}
	fields := [5]field{
		{t.Minute(), 0, 59},
		{t.Hour(), 0, 23},
		{t.Day(), 1, 31},
		{int(t.Month()), 1, 12},
		{int(t.Weekday()), 0, 6},
	}
	// Node 版は全フィールドを評価してから AND するので、エラーはどのフィールドでも返る
	all := true
	for i, f := range fields {
		ok, err := matchCronPart(parts[i], f.value, f.min, f.max)
		if err != nil {
			return false, err
		}
		all = all && ok
	}
	return all, nil
}

// matchCronPart は Node 版 matchCronPart の移植 (癖も含めて再現)。
func matchCronPart(part string, value, min, max int) (bool, error) {
	if part == "*" {
		return true, nil
	}
	values := map[int]bool{}
	for _, p := range strings.Split(part, ",") {
		switch {
		case strings.Contains(p, "/"):
			sp := strings.Split(p, "/")
			rng, step := sp[0], sp[1]
			stepNum, ok := jsParseInt(step)
			if !ok || stepNum <= 0 {
				return false, fmt.Errorf("Invalid step value: %s", step)
			}
			start, end := min, max
			// 範囲付きステップ (1-10/2)。範囲でない数値 ("5/10") は Node 版同様 min から開始する
			if rng != "*" && strings.Contains(rng, "-") {
				rs := strings.Split(rng, "-")
				a, okA := jsParseInt(rs[0])
				b, okB := jsParseInt(rs[1])
				if !okA || !okB || a < min || b > max || a > b {
					return false, fmt.Errorf("Invalid range with step: %s", p)
				}
				start, end = a, b
			}
			for i := start; i <= end; i++ {
				if (i-start)%stepNum == 0 {
					values[i] = true
				}
			}
		case strings.Contains(p, "-"):
			rs := strings.Split(p, "-")
			a, okA := jsParseInt(rs[0])
			b, okB := jsParseInt(rs[1])
			if !okA || !okB || a < min || b > max || a > b {
				return false, fmt.Errorf("Invalid range: %s", p)
			}
			for i := a; i <= b; i++ {
				values[i] = true
			}
		default:
			n, ok := jsParseInt(p)
			if !ok || n < min || n > max {
				return false, fmt.Errorf("Invalid value: %s", p)
			}
			values[n] = true
		}
	}
	return values[value], nil
}

// jsParseInt は JavaScript の parseInt(s, 10) 相当 (先頭空白・符号・先頭の数字列のみ)。
// 数字が 1 つもなければ NaN 相当として ok=false を返す。
func jsParseInt(s string) (int, bool) {
	s = strings.TrimLeft(s, " \t\n\r\v\f")
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	n, digits := 0, 0
	for digits < len(s) && s[digits] >= '0' && s[digits] <= '9' {
		n = n*10 + int(s[digits]-'0')
		digits++
		if n > 1<<40 {
			break
		}
	}
	if digits == 0 {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}
