package agentbinding

// HandleClass identifies the Control scene selected by a fixed handle.
type HandleClass string

const (
	// HandleClassDelegation selects a Spawn, Delegate, or direct-run profile.
	HandleClassDelegation HandleClass = "delegation"
	// HandleClassSystem selects a fixed Control-managed system scene.
	HandleClassSystem HandleClass = "system"
)

// Definition is the single product catalog entry for one fixed handle.
type Definition struct {
	Handle       Handle
	Class        HandleClass
	Name         string
	Description  string
	Configurable bool
}

var definitions = []Definition{
	{
		Handle:      HandleSelf,
		Class:       HandleClassDelegation,
		Name:        "Session Default",
		Description: "Use the current Session controller model and reasoning effort.",
	},
	{
		Handle:       HandleBreeze,
		Class:        HandleClassDelegation,
		Name:         "Caelis Breeze",
		Description:  "Fast, bounded work such as lookup, focused edits, and quick checks.",
		Configurable: true,
	},
	{
		Handle:       HandleOrbit,
		Class:        HandleClassDelegation,
		Name:         "Caelis Orbit",
		Description:  "General implementation, review, and multi-file analysis.",
		Configurable: true,
	},
	{
		Handle:       HandleZenith,
		Class:        HandleClassDelegation,
		Name:         "Caelis Zenith",
		Description:  "Deep architecture, difficult debugging, and high-risk analysis.",
		Configurable: true,
	},
	{
		Handle:       HandleGuardian,
		Class:        HandleClassSystem,
		Name:         "Guardian",
		Description:  "Reviews tool approval requests and safety policy.",
		Configurable: true,
	},
	{
		Handle:       HandleReviewer,
		Class:        HandleClassSystem,
		Name:         "Reviewer",
		Description:  "Reviews current workspace changes through the fixed review scene.",
		Configurable: true,
	},
}

// Definitions returns every fixed handle in canonical presentation order.
func Definitions() []Definition {
	return append([]Definition(nil), definitions...)
}

// DelegationDefinitions returns the delegation handles in canonical order.
func DelegationDefinitions() []Definition {
	return definitionsForClass(HandleClassDelegation)
}

// SystemDefinitions returns the system-Agent handles in canonical order.
func SystemDefinitions() []Definition {
	return definitionsForClass(HandleClassSystem)
}

// DirectRunHandles returns user-addressable fixed handles. The Session-derived
// self handle is model-facing only and is not a slash command.
func DirectRunHandles() []Handle {
	out := make([]Handle, 0, 3)
	for _, definition := range definitions {
		if definition.Class == HandleClassDelegation && definition.Configurable {
			out = append(out, definition.Handle)
		}
	}
	return out
}

// IsSystem reports whether a handle selects a fixed Control system Agent.
func IsSystem(handle Handle) bool {
	definition, ok := lookupDefinition(handle)
	return ok && definition.Class == HandleClassSystem
}

// IsDelegation reports whether a handle is visible to Spawn or delegation.
// Self is included even though it is never persisted.
func IsDelegation(handle Handle) bool {
	definition, ok := lookupDefinition(handle)
	return ok && definition.Class == HandleClassDelegation
}

// IsDirectRun reports whether a handle is a user-addressable fixed profile.
func IsDirectRun(handle Handle) bool {
	definition, ok := lookupDefinition(handle)
	return ok && definition.Class == HandleClassDelegation && definition.Configurable
}

func definitionsForClass(class HandleClass) []Definition {
	out := make([]Definition, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Class == class {
			out = append(out, definition)
		}
	}
	return out
}

func lookupDefinition(handle Handle) (Definition, bool) {
	handle = NormalizeHandle(handle)
	for _, definition := range definitions {
		if definition.Handle == handle {
			return definition, true
		}
	}
	return Definition{}, false
}

func isPersistedHandle(handle Handle) bool {
	definition, ok := lookupDefinition(handle)
	return ok && definition.Configurable
}

func order(handle Handle) int {
	handle = NormalizeHandle(handle)
	for i, definition := range definitions {
		if definition.Handle == handle {
			return i
		}
	}
	return len(definitions)
}
