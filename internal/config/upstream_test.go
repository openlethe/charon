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

func TestRecoveryReadOnlyStrictParsing(t *testing.T) {
	t.Setenv("CHARON_MODE", "memory-git")
	t.Setenv("CHARON_PUBLIC_URL", "https://charon.example.test")
	t.Setenv("CHARON_OAUTH_REDIRECT_URIS", "https://chatgpt.com/connector/oauth/*")
	t.Setenv("CHARON_OAUTH_PAIRING_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("LETHE_API_KEY", "key")
	t.Setenv("CHARON_OBOL_HMAC_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("CHARON_MERGE_HMAC_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("CHARON_AUTH_MODE", "obol")
	t.Setenv("CHARON_OAUTH_DEFAULT_USER", "principal")

	// Invalid values fail closed instead of silently disabling the switch.
	t.Setenv("CHARON_RECOVERY_READONLY", "yes")
	if _, err := FromEnv(); err == nil {
		t.Fatal("invalid recovery read-only value accepted")
	}
	t.Setenv("CHARON_RECOVERY_READONLY", "true")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RecoveryReadOnly {
		t.Fatal("recovery read-only not parsed")
	}
}
