package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func valid() map[string]string {
	return map[string]string{
		"AUTH_API_URL": "http://auth", "USERS_API_URL": "http://users", "DB_URI": "mongodb://x/y",
	}
}

func TestLoadValid(t *testing.T) {
	cfg, err := Load(env(valid()))
	if err != nil || cfg.AuthAPIURL != "http://auth" || cfg.UsersAPIURL != "http://users" {
		t.Fatalf("cfg = %+v err = %v", cfg, err)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	_, err := Load(env(map[string]string{}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"AUTH_API_URL", "USERS_API_URL", "DB_URI"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}
