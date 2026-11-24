// Copyright (c) 2026 Deepak Vankadaru

package internal

import (
	"encoding/json"
	"testing"
)

func TestNumberString(t *testing.T) {
	for in, want := range map[string]NumberString{`"26112100001"`: "26112100001", `26112100001`: "26112100001", `null`: "", `"abc"`: "abc"} {
		var s NumberString
		if err := json.Unmarshal([]byte(in), &s); err != nil || s != want {
			t.Errorf("%s: got %q, %v; want %q", in, s, err, want)
		}
	}
	var s NumberString
	if err := json.Unmarshal([]byte(`true`), &s); err == nil {
		t.Errorf("true: got %q, want an error", s)
	}
}

func TestInt64(t *testing.T) {
	for in, want := range map[string]Int64{`1766120400000`: 1766120400000, `"1766120400000"`: 1766120400000, `""`: 0, `null`: 0, `"-5"`: -5} {
		var v Int64
		if err := json.Unmarshal([]byte(in), &v); err != nil || v != want {
			t.Errorf("%s: got %d, %v; want %d", in, v, err, want)
		}
	}
	for _, in := range []string{`"1.5"`, `1.5`, `"abc"`, `{}`} {
		var v Int64
		if err := json.Unmarshal([]byte(in), &v); err == nil {
			t.Errorf("%s: got %d, want an error", in, v)
		}
	}
}
