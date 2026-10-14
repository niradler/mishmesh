package typedfields

import "encoding/json"

func Redact(fields []Field, raw json.RawMessage) (json.RawMessage, error) {
	values, err := validatedValues(fields, raw)
	if err != nil {
		return nil, err
	}
	for _, field := range fields {
		value, exists := values[field.Key]
		if field.Kind == "secret" {
			delete(values, field.Key)
			values["has_"+field.Key] = exists && hasValue(value)
			continue
		}
		if !field.Sensitive || !exists {
			continue
		}
		if field.Kind == "keyvalue" {
			entries := value.(map[string]any)
			for key := range entries {
				entries[key] = "<redacted>"
			}
			continue
		}
		values[field.Key] = "<redacted>"
	}
	return json.Marshal(values)
}
