package config

import (
	"testing"

	"github.com/aki0429/Mirakurun/go/internal/apid"
)

func systemWith(ranges4, ranges6 []string) *System {
	return NewSystem(&apid.ConfigServer{
		AllowIPv4CidrRanges: ranges4,
		AllowIPv6CidrRanges: ranges6,
	})
}

func TestIsPermittedIPAddress(t *testing.T) {
	s := systemWith(
		[]string{"10.0.0.0/8", "127.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"},
		[]string{"fc00::/7"},
	)
	cases := []struct {
		addr string
		want bool
	}{
		{"10.1.2.3", true},
		{"192.168.1.11", true},
		{"172.16.0.1", true},
		{"172.32.0.1", false},
		{"8.8.8.8", false},
		{"fc00::1", true},
		{"fd12:3456::1", true},
		{"2001:db8::1", false},
		{"not-an-ip", false},
	}
	for _, c := range cases {
		if got := s.IsPermittedIPAddress(c.addr); got != c.want {
			t.Errorf("IsPermittedIPAddress(%q) = %v want %v", c.addr, got, c.want)
		}
	}
}

func TestIsPermittedHost(t *testing.T) {
	s := systemWith([]string{"127.0.0.0/8", "192.168.0.0/16"}, []string{"fc00::/7"})
	cases := []struct {
		url      string
		hostname string
		want     bool
	}{
		{"http://localhost:40772/api", "", true},
		{"http://192.168.1.11:40772/api", "akki", true},
		{"http://akki:40772/api", "akki", true},
		{"http://8.8.8.8/api", "akki", false},
		{"http://example.com/api", "akki", false},
	}
	for _, c := range cases {
		if got := s.IsPermittedHost(c.url, c.hostname); got != c.want {
			t.Errorf("IsPermittedHost(%q, %q) = %v want %v", c.url, c.hostname, got, c.want)
		}
	}
}

func TestCIDRValidation(t *testing.T) {
	if !isValidIPv4CidrRange("10.0.0.0/8") || isValidIPv4CidrRange("10.0.0.0") || isValidIPv4CidrRange("10.0.0.0/33") || isValidIPv4CidrRange("999.0.0.0/8") {
		t.Errorf("IPv4 CIDR validation wrong")
	}
	if !isValidIPv6CidrRange("fc00::/7") || isValidIPv6CidrRange("fc00::/129") || isValidIPv6CidrRange("10.0.0.0/8") {
		t.Errorf("IPv6 CIDR validation wrong")
	}
}
