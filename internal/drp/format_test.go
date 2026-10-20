// SPDX-License-Identifier: AGPL-3.0-only

package drp

import (
	"strings"
	"testing"
)

func TestFamilyOf(t *testing.T) {
	cases := map[string]family{
		"uuid":                        famUUID,
		"boolean":                     famBool,
		"integer":                     famInt,
		"bigint":                      famInt,
		"smallint":                    famInt,
		"double precision":            famFloat,
		"real":                        famFloat,
		"character varying":           famText,
		"character varying(128)":      famText,
		"text":                        famText,
		"bytea":                       famBytes,
		"timestamp without time zone": famTime,
		"integer[]":                   famIntArray,
		"jsonb":                       famUnknown,
		"":                            famUnknown,
	}
	for typ, want := range cases {
		if got := familyOf(typ); got != want {
			t.Errorf("familyOf(%q) = %v, want %v", typ, got, want)
		}
	}
	if famUnknown.String() != "unknown" || famText.String() != "text" || famIntArray.String() != "integer array" {
		t.Error("family names")
	}
}

func TestFormatScalar(t *testing.T) {
	cases := []struct {
		in   any
		text bool
		want string
	}{
		{13336.656666666666, false, "13336.656666666666"},
		{1.0, false, "1"},
		{0.5, false, "0.5"},
		{-68719476737.0, false, "-68719476737"},
		{float32(2.5), false, "2.5"},
		{int32(7), false, "7"},
		{int64(-68719476737), false, "-68719476737"},
		{int16(3), false, "3"},
		{true, false, "true"},
		{false, false, "false"},
		{[]byte{0xff, 0xd8, 0x00}, false, "ffd800"},
		{[]byte("-68719476737"), true, "-68719476737"},
		{"hola", false, "hola"},
	}
	for _, c := range cases {
		got, err := formatScalar(c.in, c.text)
		if err != nil || got != c.want {
			t.Errorf("formatScalar(%#v, %v) = %q, %v; want %q", c.in, c.text, got, err, c.want)
		}
	}
	if _, err := formatScalar(struct{}{}, false); err == nil || !strings.Contains(err.Error(), "SQS-E006") {
		t.Errorf("unexpected type: %v", err)
	}
	if formatFloat(1e-7) != "0.0000001" {
		t.Errorf("formatFloat(1e-7) = %q", formatFloat(1e-7))
	}
}

func TestParseVector(t *testing.T) {
	got, err := parseVector("{0,1}")
	if err != nil || len(got) != 2 || got[0] != "0" || got[1] != "1" {
		t.Errorf("{0,1}: %v %v", got, err)
	}
	for _, s := range []string{"{}", "", "  "} {
		if got, err := parseVector(s); err != nil || got != nil {
			t.Errorf("%q: %v %v", s, got, err)
		}
	}
	if _, err := parseVector("0,1"); err == nil || !strings.Contains(err.Error(), "SQS-E006") {
		t.Errorf("bad vector accepted: %v", err)
	}
}

func TestEscape(t *testing.T) {
	if got := escape(`a & b < c > d "e"`); got != "a &amp; b &lt; c &gt; d &quot;e&quot;" {
		t.Errorf("escape = %q", got)
	}
	if got := escape("unchanged 'ok' ñ"); got != "unchanged 'ok' ñ" {
		t.Errorf("escape changed plain text: %q", got)
	}
}
