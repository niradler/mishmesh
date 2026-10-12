package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func MigrateService(driver string, raw json.RawMessage) (string, json.RawMessage, json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := validateUniqueJSON(decoder); err != nil {
		return "", nil, nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "", nil, nil, errors.New("legacy service spec has trailing data")
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(raw, &legacy); err != nil || legacy == nil {
		return "", nil, nil, errors.New("legacy service spec must be an object")
	}
	block := driver
	if driver == "exec" {
		driver = "command"
	}
	if block != "docker" && block != "compose" && block != "exec" {
		return "", nil, nil, errors.New("unknown legacy service driver")
	}
	for key := range legacy {
		if key != block && key != "ready_tcp" {
			return "", nil, nil, errors.New("legacy service spec contains an unexpected driver block or field")
		}
	}
	config, exists := legacy[block]
	var object map[string]json.RawMessage
	if !exists || json.Unmarshal(config, &object) != nil || object == nil {
		return "", nil, nil, errors.New("legacy service driver config must be an object")
	}
	ready := json.RawMessage(`{}`)
	if rawTCP, exists := legacy["ready_tcp"]; exists {
		var tcp string
		if bytes.Equal(bytes.TrimSpace(rawTCP), []byte("null")) || json.Unmarshal(rawTCP, &tcp) != nil {
			return "", nil, nil, errors.New("legacy readiness must be a string")
		}
		if tcp != "" {
			encoded, err := json.Marshal(struct {
				TCP string `json:"tcp"`
			}{TCP: tcp})
			if err != nil {
				return "", nil, nil, err
			}
			ready = encoded
		}
	}
	return driver, append(json.RawMessage(nil), config...), ready, nil
}

func validateUniqueJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return errors.New("invalid legacy service JSON")
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok || seen[key] {
				return errors.New("legacy service JSON contains an invalid or duplicate key")
			}
			seen[key] = true
			if err := validateUniqueJSON(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid legacy service object")
		}
	case '[':
		for decoder.More() {
			if err := validateUniqueJSON(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("invalid legacy service array")
		}
	default:
		return errors.New("invalid legacy service JSON")
	}
	return nil
}
