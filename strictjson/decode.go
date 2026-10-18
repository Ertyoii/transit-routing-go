// Package strictjson rejects duplicate/ambiguous object keys before typed decoding.
package strictjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func Decode(data []byte, v any) error {
	scanner := json.NewDecoder(bytes.NewReader(data))
	scanner.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 1024 {
			return fmt.Errorf("JSON nesting limit exceeded")
		}
		token, e := scanner.Token()
		if e != nil {
			return e
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for scanner.More() {
				key, e := scanner.Token()
				if e != nil {
					return e
				}
				name, ok := key.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				canonical := strings.ToLower(name)
				duplicate := seen[canonical]
				for existing := range seen {
					duplicate = duplicate || strings.EqualFold(existing, canonical)
				}
				if duplicate {
					return fmt.Errorf("duplicate/ambiguous JSON key %q", name)
				}
				seen[canonical] = true
				if e = value(depth + 1); e != nil {
					return e
				}
			}
			end, e := scanner.Token()
			if e != nil {
				return e
			}
			if end != json.Delim('}') {
				return fmt.Errorf("unclosed JSON object")
			}
		case '[':
			for scanner.More() {
				if e = value(depth + 1); e != nil {
					return e
				}
			}
			end, e := scanner.Token()
			if e != nil {
				return e
			}
			if end != json.Delim(']') {
				return fmt.Errorf("unclosed JSON array")
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
		return nil
	}
	if e := value(0); e != nil {
		return e
	}
	if _, e := scanner.Token(); e != io.EOF {
		return fmt.Errorf("expected exactly one JSON value: %v", e)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(v)
}
