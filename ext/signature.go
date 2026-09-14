package ext

// A function signature, in the shape SPEC 15.6 puts on the wire.
//
// This lives here, in the package an author writes against, and the host
// reads the same type. It used to live only in the host's reader, as an
// anonymous struct nothing exported could produce — so the first
// extension written against the host's own `signature.Signature`
// marshalled that instead, sent the parameter types as the integers they
// are in that package, and had every signature refused. The handshake
// then reported an extension with no functions, four steps from the
// cause.
//
// A wire format with only a reader is a wire format nobody can write.

// Signature describes one function an extension provides.
type Signature struct {
	// Module and Function are how a caller names it. For a kind with a
	// single entry point — `pillar` is `ext_pillar` — the function name
	// is fixed and the module name is the extension's.
	Module   string `json:"module"`
	Function string `json:"function"`
	// Doc is what `sys.list_extensions` and the module reference show.
	Doc string `json:"doc,omitempty"`
	// Mutates marks a function that changes the machine, which decides
	// whether a `--test` run may call it.
	Mutates bool `json:"mutates,omitempty"`
	// Platforms limits where it applies, as `<goos>` or `<goos>/<goarch>`.
	// Empty means everywhere.
	Platforms []string `json:"platforms,omitempty"`
	// Privileges names what it needs, for the operator reading a policy.
	Privileges []string `json:"privileges,omitempty"`
	Params     []Param  `json:"params,omitempty"`
}

// Param is one parameter.
type Param struct {
	Name string `json:"name"`
	// Type is the name of a type, never a number: a number would make
	// the protocol depend on the order of a const block in a package no
	// other implementation has.
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	Doc      string `json:"doc,omitempty"`
	Default  any    `json:"default,omitempty"`
}

// The parameter type names of SPEC 15.6.
//
// A host that does not know a name treats the parameter as untyped
// rather than refusing the function: a newer extension naming a type
// this host has never heard of should lose the checking, not the
// function.
const (
	TypeAny      = "any"
	TypeString   = "string"
	TypeInt      = "int"
	TypeFloat    = "float"
	TypeBool     = "bool"
	TypeList     = "list"
	TypeMap      = "map"
	TypePath     = "path"
	TypeMode     = "mode"
	TypeDuration = "duration"
)
