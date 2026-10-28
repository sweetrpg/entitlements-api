package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestRunRequiresURLAndToken(t *testing.T) {
	for _, m := range []map[string]string{{}, {"ENTITLEMENTS_API_URL": "http://x"}, {"ENTITLEMENTS_ADMIN_TOKEN": "t"}} {
		if err := run(nil, env(m), &bytes.Buffer{}); err == nil {
			t.Errorf("env %v accepted", m)
		}
	}
}

func TestRunDryRunListsEmbeddedSeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"id":""}`)) }))
	defer srv.Close()
	var out bytes.Buffer
	err := run([]string{"-dry-run"}, env(map[string]string{"ENTITLEMENTS_API_URL": srv.URL, "ENTITLEMENTS_ADMIN_TOKEN": "t"}), &out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"key initiative.sessions_per_gm", "tier free", "default tier free", "product theme-slot"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunRejectsBadFiles(t *testing.T) {
	e := env(map[string]string{"ENTITLEMENTS_API_URL": "http://x", "ENTITLEMENTS_ADMIN_TOKEN": "t"})
	if err := run([]string{"-file", filepath.Join(t.TempDir(), "missing.json")}, e, &bytes.Buffer{}); err == nil {
		t.Error("missing file accepted")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(bad, []byte(`{"nope":1}`), 0o600)
	if err := run([]string{"-file", bad}, e, &bytes.Buffer{}); err == nil {
		t.Error("unknown field accepted")
	}
	if err := run([]string{"-bogus"}, e, &bytes.Buffer{}); err == nil {
		t.Error("unknown flag accepted")
	}
}
