package utils

import (
	"net/http/httptest"
	"testing"
)

func TestForwardedClientIP(t *testing.T) {
	cases := []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{name: "no proxy", remote: "192.0.2.10:5555", want: ""},
		{name: "proxied", remote: "10.0.0.2:5555", xff: []string{"198.51.100.4"}, want: "198.51.100.4"},
		{name: "caller-supplied entries ignored", remote: "10.0.0.2:5555", xff: []string{"1.2.3.4, 198.51.100.4"}, want: "198.51.100.4"},
		{name: "last header wins", remote: "10.0.0.2:5555", xff: []string{"1.2.3.4", "198.51.100.4"}, want: "198.51.100.4"},
		{name: "empty header", remote: "192.0.2.10:5555", xff: []string{""}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/graphql", nil)
			r.RemoteAddr = tc.remote
			for _, v := range tc.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := ForwardedClientIP(r); got != tc.want {
				t.Fatalf("ForwardedClientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}
