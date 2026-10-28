package models

import (
	"errors"
	"testing"
	"time"
)

func TestValidateKeyName(t *testing.T) {
	good := []string{"initiative.theme_slots", "a.b.c"}
	bad := []string{"", "nodots", "Upper.case", "a..b", ".a", "a.", "1a.b"}
	for _, n := range good {
		if err := ValidateKeyName(n); err != nil {
			t.Errorf("%q rejected: %v", n, err)
		}
	}
	for _, n := range bad {
		if err := ValidateKeyName(n); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q accepted", n)
		}
	}
}

func TestValidateTierSlug(t *testing.T) {
	if err := ValidateTierSlug("free"); err != nil {
		t.Error(err)
	}
	for _, s := range []string{"", "Free", "-x", "a b"} {
		if err := ValidateTierSlug(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestParseValue(t *testing.T) {
	cases := []struct {
		typ  KeyType
		in   any
		want int64
		bad  bool
	}{
		{TypeFlag, true, 1, false}, {TypeFlag, false, 0, false}, {TypeFlag, 1.0, 0, true},
		{TypeLimit, 5.0, 5, false}, {TypeLimit, 0.0, 0, false}, {TypeLimit, -1.0, 0, true},
		{TypeLimit, 1.5, 0, true}, {TypeLimit, true, 0, true}, {TypeLimit, 1e12, 0, true},
		{KeyType("odd"), 1.0, 0, true},
	}
	for _, c := range cases {
		got, err := ParseValue(c.typ, c.in)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("ParseValue(%s,%v) = %d,%v", c.typ, c.in, got, err)
		}
	}
}

func TestFormatValue(t *testing.T) {
	if FormatValue(TypeFlag, 1) != true || FormatValue(TypeFlag, 0) != false {
		t.Error("flag format")
	}
	if FormatValue(TypeLimit, 7) != int64(7) {
		t.Error("limit format")
	}
}

func TestGrantActiveAt(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	cases := []struct {
		g    Grant
		want bool
	}{
		{Grant{Status: GrantActive}, true},
		{Grant{Status: GrantActive, ValidUntil: &future}, true},
		{Grant{Status: GrantActive, ValidUntil: &past}, false},
		{Grant{Status: GrantEnded, ValidUntil: &future}, false},
	}
	for i, c := range cases {
		if got := c.g.ActiveAt(now); got != c.want {
			t.Errorf("case %d = %v", i, got)
		}
	}
}

func TestValidateMax(t *testing.T) {
	five, zero := int64(5), int64(0)
	cases := []struct {
		name string
		typ  KeyType
		max  *int64
		ok   bool
	}{
		{"none on limit", TypeLimit, nil, true},
		{"none on flag", TypeFlag, nil, true},
		{"positive on limit", TypeLimit, &five, true},
		{"on flag", TypeFlag, &five, false},
		{"zero", TypeLimit, &zero, false},
	}
	for _, c := range cases {
		if err := ValidateMax(c.typ, c.max); (err == nil) != c.ok {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
}
