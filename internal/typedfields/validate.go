package typedfields

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
)

func Validate(fields []Field, raw json.RawMessage) error {
	_, err := validatedValues(fields, raw)
	return err
}

func UsesHostCommands(fields []Field, raw json.RawMessage) (bool, error) {
	values, err := validatedValues(fields, raw)
	if err != nil {
		return false, err
	}
	for _, field := range fields {
		value, exists := values[field.Key]
		if field.HostCommand && exists && hasValue(value) {
			return true, nil
		}
	}
	return false, nil
}

func validatedValues(fields []Field, raw json.RawMessage) (map[string]any, error) {
	keys := make(map[string]bool, len(fields))
	defaults := make(map[string]any, len(fields))
	for _, field := range fields {
		if strings.TrimSpace(field.Key) == "" {
			return nil, errors.New("field descriptor requires a key")
		}
		if keys[field.Key] {
			return nil, errors.New("duplicate field descriptor key")
		}
		keys[field.Key] = true
		switch field.Kind {
		case "text", "list", "keyvalue", "secret", "select", "number", "bool", "connection":
		default:
			return nil, fmt.Errorf("field %q: unsupported kind", field.Key)
		}
		if len(field.Default) != 0 {
			value, err := validateValue(field, field.Default)
			if err != nil {
				return nil, fmt.Errorf("field %q: invalid default", field.Key)
			}
			defaults[field.Key] = value
		}
	}
	for _, field := range fields {
		if field.Kind == "secret" && keys["has_"+field.Key] {
			return nil, errors.New("field descriptor conflicts with a secret presence flag")
		}
	}
	object, err := decodeObject(raw)
	if err != nil {
		return nil, err
	}
	for key := range object {
		if !keys[key] {
			return nil, errors.New("unknown configuration field")
		}
	}
	values := make(map[string]any, len(fields))
	for _, field := range fields {
		value, exists := defaults[field.Key]
		if supplied, present := object[field.Key]; present {
			value, err = validateValue(field, supplied)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", field.Key, err)
			}
			exists = true
		}
		if field.Required && (!exists || isEmpty(value)) {
			return nil, fmt.Errorf("field %q: required value is missing or empty", field.Key)
		}
		if exists {
			values[field.Key] = value
		}
	}
	return values, nil
}

func decodeObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("configuration must be a JSON object")
	}
	object := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, errors.New("invalid JSON object")
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid JSON object key")
		}
		if _, exists := object[key]; exists {
			return nil, errors.New("duplicate JSON object key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("invalid JSON field value")
		}
		object[key] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("invalid JSON object")
	}
	if err := ensureEnd(decoder); err != nil {
		return nil, err
	}
	return object, nil
}

func decodeValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, errors.New("invalid JSON value")
	}
	if err := ensureEnd(decoder); err != nil {
		return nil, err
	}
	return value, nil
}

func ensureEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}

func validateValue(field Field, raw json.RawMessage) (any, error) {
	value, err := decodeValue(raw)
	if err != nil {
		return nil, err
	}
	switch field.Kind {
	case "text", "secret", "connection", "select":
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("must be a string")
		}
		if field.Kind == "select" && !isEmpty(text) && !slices.Contains(field.Options, text) {
			return nil, errors.New("must be a listed option")
		}
	case "list":
		items, ok := value.([]any)
		if !ok {
			return nil, errors.New("must be a list of strings")
		}
		for _, item := range items {
			if _, ok := item.(string); !ok {
				return nil, errors.New("must be a list of strings")
			}
		}
	case "keyvalue":
		entries, err := decodeObject(raw)
		if err != nil {
			return nil, errors.New("must be an object of strings with unique keys")
		}
		for _, entry := range entries {
			entryValue, err := decodeValue(entry)
			if err != nil {
				return nil, errors.New("must be an object of strings")
			}
			if _, ok := entryValue.(string); !ok {
				return nil, errors.New("must be an object of strings")
			}
		}
	case "number":
		number, ok := value.(json.Number)
		if !ok {
			return nil, errors.New("must be a finite number")
		}
		parsed, err := number.Float64()
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return nil, errors.New("must be a finite number")
		}
	case "bool":
		if _, ok := value.(bool); !ok {
			return nil, errors.New("must be a boolean")
		}
	}
	return value, nil
}

func isEmpty(value any) bool {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text) == ""
	}
	return !hasValue(value)
}

func hasValue(value any) bool {
	switch typed := value.(type) {
	case string:
		return typed != ""
	case []any:
		return len(typed) != 0
	case map[string]any:
		return len(typed) != 0
	default:
		return value != nil
	}
}
