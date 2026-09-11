package render

// The seam the render sandbox of SPEC section 25.4 needs.
//
// SPEC puts all YAML parsing and all template rendering in an
// unprivileged child process, on the argument that "the parser and the
// template engine are the largest and most attacker-adjacent code in the
// system, and they need no privilege at all". That is a property of
// *where* a render happens rather than of what it produces, so the
// compiler asks an Engine rather than calling Render directly, and an
// Engine that is nil means this process does it itself.
//
// The split below is what makes the sandbox implementable without giving
// the child a keyring. A pipeline is always template stages, then one
// serializer, then data stages -- `checkStages` enforces exactly that
// shape -- and the only data stage this build supports is `gpg`. So the
// child runs the head, which is the parsing and the rendering, and the
// parent runs the tail, which is the decryption. Plaintext pillar never
// enters the unprivileged process at all, which is a better answer than
// sending it there and back.

// Engine renders one file.
//
// The implementations are InProcess, which is this process, and the
// sandbox in internal/rendersandbox, which is a child.
type Engine interface {
	Render(src []byte, opts Options) (Result, error)
	// Template is the other half of SPEC 25.4's "all template
	// rendering": a `file.managed` source with `template: jinja` is
	// rendered through this rather than through Render, because the
	// file being managed is not an SLS file and its first line is
	// content. It is attacker-adjacent for the same reason -- it comes
	// off the file server -- so it belongs on the same side of the
	// boundary.
	Template(src []byte, opts Options) (string, []Warning, error)
	// Describe says what this engine actually confines, in the manner
	// of the bridge sandbox: an operator reads what is applied on the
	// machine in front of them rather than what SPEC hopes for.
	Describe() []string
}

// InProcess renders in the calling process, which is what halite did
// everywhere before the sandbox existed and still does wherever the
// sandbox is not turned on.
type InProcess struct{}

func (InProcess) Render(src []byte, opts Options) (Result, error) { return Render(src, opts) }

func (InProcess) Template(src []byte, opts Options) (string, []Warning, error) {
	return Template(src, opts)
}

func (InProcess) Describe() []string {
	return []string{"none: YAML parsing and template rendering happen in this process, as SPEC 25.4's render sandbox is not enabled"}
}

// Use returns the engine to render with, which is the in-process one
// when a caller has set none.
func Use(e Engine) Engine {
	if e == nil {
		return InProcess{}
	}
	return e
}

// CheckPipeline refuses a pipeline this build cannot run. The sandbox
// calls it before splitting one, so that an unrunnable pipeline is
// refused in the same words whether or not the sandbox is on.
func CheckPipeline(stages []string, file string) error { return checkStages(stages, file) }

// SplitPipeline divides a pipeline at its serializer: head is the
// template stages and the serializer, tail is the data stages that
// transform the parsed value.
//
// A pipeline with no serializer has no tail, because a data stage
// without one is refused by checkStages before anything runs.
func SplitPipeline(stages []string) (head, tail []string) {
	for i, stage := range stages {
		if Stages[stage].Kind == "serializer" {
			return stages[:i+1], stages[i+1:]
		}
	}
	return stages, nil
}

// ApplyDataStages runs the data stages of a pipeline over an
// already-serialized value. It is the tail the sandbox leaves to the
// privileged parent.
func ApplyDataStages(v any, stages []string, opts Options) (any, error) {
	for _, stage := range stages {
		switch stage {
		case "gpg":
			out, err := decryptGPG(v, opts)
			if err != nil {
				return nil, err
			}
			v = out
		default:
			// Unreachable through Render, which checks the whole
			// pipeline first. Reachable by a caller that assembles a
			// stage list itself, and a wrong answer there is a pillar
			// delivered as ciphertext.
			return nil, errUnknownStage(opts.File, stage)
		}
	}
	return v, nil
}
