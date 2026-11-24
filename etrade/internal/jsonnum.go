// Copyright (c) 2026 Deepak Vankadaru

package internal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// NumberString holds a JSON value that E*TRADE may send either as a number or
// as a string, such as a transaction ID: the documentation says long, but the
// API sends a string. It keeps the value's text.
type NumberString string

// UnmarshalJSON accepts a JSON string, a JSON number or null.
func (s *NumberString) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		var v string
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		*s = NumberString(v)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("want a string or a number: %w", err)
	}
	*s = NumberString(n.String())
	return nil
}

// Int64 is an integer that E*TRADE may send either as a JSON number or as a
// string holding one.
type Int64 int64

// UnmarshalJSON accepts a JSON number, a string holding an integer (an empty
// string is zero) or null.
func (v *Int64) UnmarshalJSON(data []byte) error {
	var s NumberString
	if err := s.UnmarshalJSON(data); err != nil {
		return err
	}
	if s == "" {
		*v = 0
		return nil
	}
	n, err := strconv.ParseInt(string(s), 10, 64)
	if err != nil {
		return fmt.Errorf("want an integer: %w", err)
	}
	*v = Int64(n)
	return nil
}
