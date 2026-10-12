package typedfields

import "encoding/json"

type Field struct {
	Key         string          `json:"key"`
	Label       string          `json:"label"`
	Kind        string          `json:"kind"`
	Options     []string        `json:"options,omitempty"`
	ConnType    string          `json:"conn_type,omitempty"`
	Required    bool            `json:"required"`
	Placeholder string          `json:"placeholder,omitempty"`
	Help        string          `json:"help,omitempty"`
	HostCommand bool            `json:"host_command,omitempty"`
	Sensitive   bool            `json:"sensitive,omitempty"`
	Default     json.RawMessage `json:"default,omitempty"`
}
