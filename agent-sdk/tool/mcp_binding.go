package tool

// Binding identifies a canonical remote tool independently of its projected
// name. Version binds configuration, implementation and schema, using the MCP
// source fingerprint; a same-name replacement cannot inherit discovery.
type Binding struct {
	Name     string `json:"name"`
	Identity string `json:"identity"`
	Version  string `json:"version"`
}
