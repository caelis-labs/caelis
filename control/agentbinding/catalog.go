package agentbinding

// HandleClass identifies the Control scene selected by a handle.
type HandleClass string

const (
	// HandleClassDelegation selects a Spawn, Delegate, or direct-run profile.
	HandleClassDelegation HandleClass = "delegation"
	// HandleClassSystem selects a fixed Control-managed system scene.
	HandleClassSystem HandleClass = "system"
)

// Definition is one fixed or user-created handle catalog entry.
type Definition struct {
	Handle       Handle
	Class        HandleClass
	Name         string
	Description  string
	Configurable bool
	Custom       bool
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
	{
		Handle:       HandleSteward,
		Class:        HandleClassSystem,
		Name:         "Memory Steward",
		Description:  "Organizes Memory semantically when explicitly bound; unbound Memory remains static and token-free.",
		Configurable: true,
	},
}

// Catalog is one closed view of the fixed handle definitions plus the custom
// roles in a specific Agent binding configuration.
type Catalog struct {
	definitions []Definition
}

// CatalogFor returns a detached handle catalog for configuration.
func CatalogFor(configuration Configuration) Catalog {
	delegation := definitionsForClass(definitions, HandleClassDelegation)
	for _, role := range normalizedRoles(configuration.Roles) {
		delegation = append(delegation, Definition{
			Handle:       role.Handle,
			Class:        HandleClassDelegation,
			Name:         string(role.Handle),
			Description:  role.Description,
			Configurable: true,
			Custom:       true,
		})
	}
	return Catalog{
		definitions: append(delegation, definitionsForClass(definitions, HandleClassSystem)...),
	}
}

// Definitions returns every definition in canonical presentation order.
func (c Catalog) Definitions() []Definition {
	return append([]Definition(nil), c.definitions...)
}

// DelegationDefinitions returns every delegation definition in canonical
// presentation order.
func (c Catalog) DelegationDefinitions() []Definition {
	return definitionsForClass(c.definitions, HandleClassDelegation)
}

// SystemDefinitions returns every system-Agent definition in canonical order.
func (c Catalog) SystemDefinitions() []Definition {
	return definitionsForClass(c.definitions, HandleClassSystem)
}

// DirectRunHandles returns every user-addressable delegation handle in
// canonical order.
func (c Catalog) DirectRunHandles() []Handle {
	out := make([]Handle, 0, len(c.definitions))
	for _, definition := range c.definitions {
		if IsDirectRunDefinition(definition) {
			out = append(out, definition.Handle)
		}
	}
	return out
}

// Lookup returns one definition from the closed catalog.
func (c Catalog) Lookup(handle Handle) (Definition, bool) {
	handle = NormalizeHandle(handle)
	for _, definition := range c.definitions {
		if definition.Handle == handle {
			return definition, true
		}
	}
	return Definition{}, false
}

// IsDelegation reports whether handle is a delegation target in the catalog.
func (c Catalog) IsDelegation(handle Handle) bool {
	definition, ok := c.Lookup(handle)
	return ok && definition.Class == HandleClassDelegation
}

// IsDirectRun reports whether handle may be invoked directly in the catalog.
func (c Catalog) IsDirectRun(handle Handle) bool {
	definition, ok := c.Lookup(handle)
	return ok && IsDirectRunDefinition(definition)
}

// Definitions returns every fixed handle in canonical presentation order.
func Definitions() []Definition {
	return CatalogFor(Configuration{}).Definitions()
}

// DelegationDefinitions returns the delegation handles in canonical order.
func DelegationDefinitions() []Definition {
	return CatalogFor(Configuration{}).DelegationDefinitions()
}

// SystemDefinitions returns the system-Agent handles in canonical order.
func SystemDefinitions() []Definition {
	return CatalogFor(Configuration{}).SystemDefinitions()
}

// DirectRunHandles returns user-addressable fixed handles. The Session-derived
// self handle is model-facing only and is not a slash command.
func DirectRunHandles() []Handle {
	return CatalogFor(Configuration{}).DirectRunHandles()
}

// IsSystem reports whether a handle selects a fixed Control system Agent.
func IsSystem(handle Handle) bool {
	definition, ok := lookupDefinition(handle)
	return ok && definition.Class == HandleClassSystem
}

// IsDelegation reports whether a handle is visible to Spawn or delegation.
// Self is included even though it is never persisted.
func IsDelegation(handle Handle) bool {
	return CatalogFor(Configuration{}).IsDelegation(handle)
}

// IsDirectRun reports whether a handle is a user-addressable fixed profile.
func IsDirectRun(handle Handle) bool {
	return CatalogFor(Configuration{}).IsDirectRun(handle)
}

// IsDirectRunDefinition reports whether one detached definition is a
// user-addressable delegation role.
func IsDirectRunDefinition(definition Definition) bool {
	return definition.Class == HandleClassDelegation && definition.Configurable
}

func definitionsForClass(in []Definition, class HandleClass) []Definition {
	out := make([]Definition, 0, len(in))
	for _, definition := range in {
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
