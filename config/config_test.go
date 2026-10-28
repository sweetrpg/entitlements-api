package config

import (
	"strings"
	"testing"
)

const tok1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const tok2 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func valid() map[string]string {
	return map[string]string{
		"AUTH_API_URL": "http://auth", "USERS_API_URL": "http://users",
		"DB_URI": "mongodb://x/y", "SERVICE_TOKENS": "initiative-api=" + tok1 + ",other=" + tok2,
	}
}

func TestLoadValid(t *testing.T) {
	cfg, err := Load(env(valid()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceTokens[tok1] != "initiative-api" || cfg.ServiceTokens[tok2] != "other" {
		t.Errorf("tokens = %v", cfg.ServiceTokens)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	_, err := Load(env(map[string]string{}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"AUTH_API_URL", "USERS_API_URL", "DB_URI", "SERVICE_TOKENS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

func TestServiceTokensRejected(t *testing.T) {
	cases := map[string]string{
		"no equals":       "initiative-api",
		"empty name":      "=" + tok1,
		"short token":     "a=short",
		"duplicate name":  "a=" + tok1 + ",a=" + tok2,
		"duplicate token": "a=" + tok1 + ",b=" + tok1,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			m := valid()
			m["SERVICE_TOKENS"] = raw
			if _, err := Load(env(m)); err == nil {
				t.Errorf("expected error for %q", raw)
			}
		})
	}
}
