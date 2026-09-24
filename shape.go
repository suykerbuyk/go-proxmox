package proxmox

import (
	"errors"
	"fmt"
)

// ErrUnexpectedShape matches every *ShapeError: a PVE response carried a
// field of a type the decoder cannot read.
var ErrUnexpectedShape = errors.New("unexpected shape in a PVE response")

// ShapeError reports a field of an unexpected JSON type in a PVE response,
// where the decoder used to panic. A null or absent field is not one: it
// leaves the field at its zero value, as encoding/json does.
type ShapeError struct {
	// Type is the Go type being decoded, e.g. "Task".
	Type string
	// Field is the JSON key.
	Field string
	// Want and Got are JSON kinds: "string", "number", "bool", "object"
	// or "array".
	Want, Got string
}

func (e *ShapeError) Error() string {
	return fmt.Sprintf("proxmox: decode %s: field %q is %s, want %s", e.Type, e.Field, e.Got, e.Want)
}

// Unwrap makes errors.Is(err, ErrUnexpectedShape) true.
func (e *ShapeError) Unwrap() error { return ErrUnexpectedShape }

// jsonKind names the JSON kind of a value decoded into an interface{}.
func jsonKind(v interface{}) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	case map[string]interface{}:
		return "object"
	case []interface{}:
		return "array"
	}
	return fmt.Sprintf("%T", v)
}

// numberField reads key from m as a JSON number. ok is false when the key
// is absent or null; a value of any other type is a *ShapeError.
func numberField(typ string, m map[string]interface{}, key string) (n float64, ok bool, err error) {
	v, present := m[key]
	if !present || v == nil {
		return 0, false, nil
	}
	n, ok = v.(float64)
	if !ok {
		return 0, false, &ShapeError{Type: typ, Field: key, Want: "number", Got: jsonKind(v)}
	}
	return n, true, nil
}

// stringField reads key from m as a JSON string, as numberField reads a
// number.
func stringField(typ string, m map[string]interface{}, key string) (s string, ok bool, err error) {
	v, present := m[key]
	if !present || v == nil {
		return "", false, nil
	}
	s, ok = v.(string)
	if !ok {
		return "", false, &ShapeError{Type: typ, Field: key, Want: "string", Got: jsonKind(v)}
	}
	return s, true, nil
}
