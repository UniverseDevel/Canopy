// Package format renders data as JSON, YAML or flat path = value lines.
package format

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// JSON renders v indented.
func JSON(v any) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return b.String(), nil
}

// YAML renders v. json.Number values become YAML numbers.
func YAML(v any) (string, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(numbers(v)); err != nil {
		return "", err
	}
	enc.Close()
	return b.String(), nil
}

func numbers(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = numbers(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = numbers(vv)
		}
		return out
	case json.Number:
		s := string(x)
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i
		}
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return u
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
		return s
	}
	return v
}

// Scalar renders a leaf value for flat output.
func Scalar(v any) string {
	switch x := v.(type) {
	case string:
		return strconv.Quote(x)
	case nil:
		return "null"
	case json.Number:
		return string(x)
	case map[string]any, []any:
		b, _ := json.Marshal(x)
		return string(b)
	}
	return fmt.Sprint(v)
}

// FlatLine is a path and value.
type FlatLine struct {
	Path  string
	Value any
}

// Flat renders lines as "path = value".
func Flat(lines []FlatLine) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Path)
		b.WriteString(" = ")
		b.WriteString(Scalar(l.Value))
		b.WriteByte('\n')
	}
	return b.String()
}
