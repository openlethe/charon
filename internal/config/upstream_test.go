package config

import "testing"

func TestValidateUpstreamTransport(t *testing.T) {
	cases := []struct {
		name          string
		url           string
		allowInsecure bool
		wantErr       bool
	}{
		{"loopback http", "http://127.0.0.1:18485", false, false},
		{"localhost http", "http://localhost:18485", false, false},
		{"ipv6 loopback http", "http://[::1]:18485", false, false},
		{"https any host", "https://lethe.internal.example", false, false},
		{"plaintext private ip", "http://192.168.1.10:18485", false, true},
		{"plaintext public dns", "http://lethe.example.com", false, true},
		{"plaintext with override", "http://192.168.1.10:18485", true, false},
		{"unix socket", "unix:///var/run/lethe.sock", false, false},
		{"unix relative", "unix://tmp/lethe.sock", false, true},
		{"ftp scheme", "ftp://lethe.example.com", false, true},
		{"userinfo rejected", "https://user:pass@lethe.example.com", false, true},
		{"empty", "", false, true},
	}
	for _, tc := range cases {
		err := validateUpstreamTransport(tc.url, tc.allowInsecure)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr = %v", tc.name, err, tc.wantErr)
		}
	}
}
