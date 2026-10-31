package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Binding identifies a canonical remote tool independently of its projected
// name. Version binds configuration, implementation and schema, using the MCP
// source fingerprint; a same-name replacement cannot inherit discovery.
type Binding struct {
	Name     string `json:"name"`
	Identity string `json:"identity"`
	Version  string `json:"version"`
}

// BindingForDefinition returns an opaque source identity and definition version.
// Incomplete external definitions cannot participate in version-bound discovery.
func BindingForDefinition(def Definition) Binding {
	field := func(k string) string { value, _ := def.Metadata[k].(string); return value }
	remote, server, version := field(MetadataMCPTool), field(MetadataMCPServer), field(MetadataMCPSourceFingerprint)
	if !IsMCPDefinition(def) || remote == "" || server == "" || version == "" {
		return Binding{}
	}
	raw, _ := json.Marshal([]string{field(MetadataPluginID), server, remote})
	sum := sha256.Sum256(raw)
	return Binding{Name: def.Name, Identity: hex.EncodeToString(sum[:]), Version: version}
}
