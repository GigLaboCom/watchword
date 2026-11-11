package clientip

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFromRequestWith(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string][]string
		remote  string
		order   []string
		want    string
	}{
		{
			name: "cf-connecting-ip wins",
			headers: map[string][]string{
				"Cf-Connecting-Ip": {"198.51.100.1"},
				"X-Forwarded-For":  {"198.51.100.2, 104.16.0.1"},
				"X-Real-Ip":        {"104.16.0.1"},
			},
			remote: "10.42.0.5:51234",
			want:   "198.51.100.1",
		},
		{
			name: "x-forwarded-for first entry when no cf header",
			headers: map[string][]string{
				"X-Forwarded-For": {"198.51.100.2, 104.16.0.1"},
				"X-Real-Ip":       {"104.16.0.1"},
			},
			remote: "10.42.0.5:51234",
			want:   "198.51.100.2",
		},
		{
			name: "x-forwarded-for first entry across repeated header lines",
			headers: map[string][]string{
				"X-Forwarded-For": {"198.51.100.2", "104.16.0.1"},
			},
			remote: "10.42.0.5:51234",
			want:   "198.51.100.2",
		},
		{
			name:    "x-real-ip last",
			headers: map[string][]string{"X-Real-Ip": {"198.51.100.3"}},
			remote:  "10.42.0.5:51234",
			want:    "198.51.100.3",
		},
		{
			name: "unknown, empty and garbage skipped",
			headers: map[string][]string{
				"Cf-Connecting-Ip": {"unknown"},
				"X-Forwarded-For":  {" , 198.51.100.9"},
				"X-Real-Ip":        {"not-an-ip"},
			},
			remote: "10.42.0.5:51234",
			want:   "10.42.0.5",
		},
		{
			name:    "ipv4 port stripped",
			headers: map[string][]string{"Cf-Connecting-Ip": {"198.51.100.1:5678"}},
			want:    "198.51.100.1",
		},
		{
			name:    "bracketed ipv6 port stripped",
			headers: map[string][]string{"X-Forwarded-For": {"[2001:db8::1]:443, 104.16.0.1"}},
			want:    "2001:db8::1",
		},
		{
			name:    "plain ipv6",
			headers: map[string][]string{"Cf-Connecting-Ip": {" 2001:db8::2 "}},
			want:    "2001:db8::2",
		},
		{
			name:    "ipv4-mapped ipv6 unmapped",
			headers: map[string][]string{"Cf-Connecting-Ip": {"::ffff:198.51.100.4"}},
			want:    "198.51.100.4",
		},
		{
			name:   "socket fallback strips port",
			remote: "203.0.113.7:4321",
			want:   "203.0.113.7",
		},
		{
			name:   "socket fallback ipv6",
			remote: "[2001:db8::7]:4321",
			want:   "2001:db8::7",
		},
		{
			name:   "invalid socket address returned as is",
			remote: "pipe",
			want:   "pipe",
		},
		{
			name: "custom order changes priority",
			headers: map[string][]string{
				"Cf-Connecting-Ip": {"198.51.100.1"},
				"X-Real-Ip":        {"198.51.100.3"},
			},
			order: []string{"X-Real-IP", "cf-connecting-ip"},
			want:  "198.51.100.3",
		},
		{
			name:    "headers not in custom order are ignored",
			headers: map[string][]string{"Cf-Connecting-Ip": {"198.51.100.1"}},
			remote:  "203.0.113.7:4321",
			order:   []string{"x-real-ip"},
			want:    "203.0.113.7",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remote
			for k, vs := range tt.headers {
				for _, v := range vs {
					r.Header.Add(k, v)
				}
			}
			order := tt.order
			if order == nil {
				order = DefaultHeaders
			}
			if got := FromRequestWith(r, order); got != tt.want {
				t.Errorf("FromRequestWith() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseHeaders(t *testing.T) {
	if got := ParseHeaders(""); len(got) != 3 || got[0] != "cf-connecting-ip" {
		t.Errorf("ParseHeaders(\"\") = %v, want defaults", got)
	}
	if got := ParseHeaders(" , "); len(got) != 3 {
		t.Errorf("ParseHeaders(blank) = %v, want defaults", got)
	}
	got := ParseHeaders(" X-Real-IP , ,True-Client-IP")
	if len(got) != 2 || got[0] != "X-Real-IP" || got[1] != "True-Client-IP" {
		t.Errorf("ParseHeaders() = %v", got)
	}
}

func TestFromRequestReadsEnv(t *testing.T) {
	t.Setenv(EnvVar, "x-real-ip")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Cf-Connecting-Ip", "198.51.100.1")
	r.Header.Set("X-Real-Ip", "198.51.100.3")
	if got := FromRequest(r); got != "198.51.100.3" {
		t.Errorf("FromRequest() = %q, want X-Real-IP value from env override", got)
	}
}
