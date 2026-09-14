// Package extconform checks an extension against the protocol.
//
// SPEC 24.2 specifies the bridge, `docs/extensions.md` writes it out in
// enough detail to implement, and `github.com/edlitmus/halite/ext`
// implements it for a Go author. None of that tells somebody who wrote
// one in another language whether they got it right. They find out from
// whatever the host happens to complain about, which is a different
// thing from being checked: the host is written to run extensions, not
// to diagnose them, and its complaints surface a long way from their
// cause.
//
// So this drives a candidate through the protocol deliberately — a good
// call, a call that cannot succeed, a version it should refuse, a
// shutdown it should honour — and reports each rule by name with what
// happened and why the rule is there.
//
// It speaks the wire directly rather than going through the host's pool,
// for two reasons. It has to send frames a host would never send, which
// is most of what there is to check. And it has to read a malformed
// answer well enough to describe it: a parameter type sent as a number
// fails the host's decoder outright, and "the frame is not readable" is
// a worse answer than naming the parameter.
package extconform

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/edlitmus/halite/ext"
)

// Status is what became of one rule.
type Status string

const (
	// Pass means the extension did what the protocol asks.
	Pass Status = "pass"
	// Fail means it did not.
	Fail Status = "fail"
	// Skip means this run could not tell, and says why. A skip is never
	// a quiet pass: an operator reading one has to be able to see what
	// was not established.
	Skip Status = "skip"
)

// Result is one rule and what became of it.
type Result struct {
	// Rule is the stable name, `group/rule`, so a failure can be
	// referred to in a bug report.
	Rule string
	// Title is the rule in one line, phrased as what a conforming
	// extension does.
	Title  string
	Status Status
	// Detail is what actually happened. Empty on a pass.
	Detail string
	// Why is what the rule is for, printed when it fails. A rule an
	// author cannot see the point of is a rule they work around.
	Why string
}

// Options configure a run.
type Options struct {
	// Path is the executable.
	Path string
	// Kind is what the host asks for. Empty asks for nothing, which
	// every extension should accept — and means the kind rules cannot
	// be checked.
	Kind string
	// Function is the one to call. Empty takes the first the extension
	// announces.
	Function string
	// Kwargs is what to call it with. A call that fails for want of an
	// argument still tells us how the extension answers.
	Kwargs any
	// Timeout bounds one exchange. Zero takes DefaultTimeout.
	Timeout time.Duration
	// Env is the child's environment. Nil takes a clean one.
	Env []string
}

// DefaultTimeout bounds one exchange.
//
// Short. Every check here is one round trip against a process that has
// already started, and a conformance run that takes a minute per rule
// is one nobody runs. An extension doing real work on a call needs
// longer and says so.
const DefaultTimeout = 10 * time.Second

func (o Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return DefaultTimeout
	}
	return o.Timeout
}

// Run checks one extension and reports every rule.
//
// An error is returned only when nothing could be established at all —
// the file will not start. Everything else is a Result, because "it
// failed rule seven" is the answer, not an error.
func Run(ctx context.Context, opts Options) ([]Result, error) {
	if opts.Path == "" {
		return nil, fmt.Errorf("no extension to check")
	}
	var out []Result

	// One session for the handshake and everything that follows from
	// it, because that is the sequence a host performs.
	main, err := start(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer main.close()

	hello, results := checkHandshake(main, opts)
	out = append(out, results...)
	if hello == nil {
		// Nothing after this can be established, and reporting a dozen
		// skips would bury the one fact that matters.
		out = append(out, skipped("call/*", "the call rules",
			"the handshake did not complete, so nothing could be called"))
		return out, nil
	}

	out = append(out, checkCalls(main, opts, hello)...)
	out = append(out, checkShutdown(main, opts))

	// Fresh processes: a handshake has already happened on the one
	// above, and these are about how it begins.
	out = append(out, checkProtocolRefusal(ctx, opts))
	out = append(out, checkKindRefusal(ctx, opts))

	return out, nil
}

// Failed reports whether any rule failed, which is what an exit code
// is made of.
func Failed(results []Result) bool {
	for _, r := range results {
		if r.Status == Fail {
			return true
		}
	}
	return false
}

// Counts summarises a run.
func Counts(results []Result) (pass, fail, skip int) {
	for _, r := range results {
		switch r.Status {
		case Pass:
			pass++
		case Fail:
			fail++
		case Skip:
			skip++
		}
	}
	return pass, fail, skip
}

func passed(rule, title string) Result {
	return Result{Rule: rule, Title: title, Status: Pass}
}

func failed(rule, title, detail, why string) Result {
	return Result{Rule: rule, Title: title, Status: Fail, Detail: detail, Why: why}
}

func skipped(rule, title, detail string) Result {
	return Result{Rule: rule, Title: title, Status: Skip, Detail: detail}
}

// checkHandshake performs the opening exchange and reads what came back.
func checkHandshake(s *session, opts Options) (*looseFrame, []Result) {
	var out []Result

	if err := s.write(ext.Frame{
		Kind: ext.FrameHello, Protocol: ext.ProtocolVersion, Ext: opts.Kind,
	}); err != nil {
		return nil, []Result{failed("handshake/answers", "it answers hello with hello_ok",
			"the hello frame could not be written: "+err.Error(),
			"The host opens every session this way. An extension that cannot be written to "+
				"has exited, or is not speaking this protocol at all.")}
	}

	frame, err := s.read()
	if err != nil {
		return nil, []Result{failed("handshake/answers", "it answers hello with hello_ok",
			describeReadFailure(err, s),
			"The first frame a host sends is hello and the only acceptable answer is hello_ok. "+
				"A frame that cannot be read is most often a print() to stdout: stdout is the "+
				"protocol, and everything for a person goes to stderr.")}
	}
	if frame.Kind != ext.FrameHelloOK {
		return nil, []Result{failed("handshake/answers", "it answers hello with hello_ok",
			fmt.Sprintf("it answered with a %q frame", frame.Kind),
			"The host has nothing to do with an extension that will not complete a handshake, "+
				"and treats anything else as a protocol violation.")}
	}
	out = append(out, passed("handshake/answers", "it answers hello with hello_ok"))

	if frame.Name == "" {
		out = append(out, failed("handshake/names-itself", "it names itself",
			"the hello_ok frame carries no name",
			"The host keys everything on the name: the cache directory, the pin, the "+
				"`ext_pillar` entry that selects it. An extension with no name cannot be "+
				"configured, only installed."))
	} else {
		out = append(out, passed("handshake/names-itself", "it names itself"))
	}

	if frame.Version == "" {
		out = append(out, failed("handshake/declares-a-version", "it declares a version",
			"the hello_ok frame carries no version",
			"`extension_pins` fixes an extension by version and digest. One with no version "+
				"cannot be pinned, so a file server change can swap it silently — which is "+
				"the thing pinning exists to stop."))
	} else {
		out = append(out, passed("handshake/declares-a-version", "it declares a version"))
	}

	out = append(out, checkSignatures(frame)...)
	return frame, out
}

// checkSignatures reports what the announced functions got wrong.
func checkSignatures(frame *looseFrame) []Result {
	const (
		announceRule = "handshake/announces-functions"
		namedRule    = "handshake/signatures-are-named"
		typedRule    = "handshake/parameters-are-typed"
	)
	announceTitle := "it announces at least one function"
	namedTitle := "every signature names a module and a function"
	typedTitle := "every parameter declares a name and a type"

	if len(frame.Functions) == 0 {
		return []Result{
			failed(announceRule, announceTitle,
				"it announced no functions",
				"A host routes a call by function name, so an extension that announces none "+
					"can be started and never used. This is also what a *rejected* signature "+
					"looks like from the outside, so check the frame as well as the code."),
			skipped(namedRule, namedTitle, "there are no signatures to check"),
			skipped(typedRule, typedTitle, "there are no signatures to check"),
		}
	}
	out := []Result{passed(announceRule, announceTitle)}

	var unnamed, untyped []string
	for i, sig := range frame.Functions {
		where := fmt.Sprintf("function %d", i+1)
		if sig.Function != "" {
			where = sig.Function + "()"
		}
		if sig.Module == "" || sig.Function == "" {
			unnamed = append(unnamed, where)
		}
		for j, p := range sig.Params {
			switch {
			case p.Name == "":
				untyped = append(untyped, fmt.Sprintf("%s: parameter %d has no name", where, j+1))
			case len(p.Type) == 0:
				untyped = append(untyped, fmt.Sprintf("%s: %s declares no type", where, p.Name))
			case !isJSONString(p.Type):
				// The mistake worth naming precisely, because it is the
				// one an enum makes for you.
				untyped = append(untyped, fmt.Sprintf(
					"%s: %s declares its type as %s, which is not a string",
					where, p.Name, p.Type))
			}
		}
	}

	if len(unnamed) > 0 {
		out = append(out, failed(namedRule, namedTitle,
			strings.Join(unnamed, "; ")+" names no module or no function",
			"A signature is how a host and an operator refer to a function. One with no "+
				"name cannot be called, documented, or granted in a policy."))
	} else {
		out = append(out, passed(namedRule, namedTitle))
	}

	if len(untyped) > 0 {
		out = append(out, failed(typedRule, typedTitle,
			strings.Join(untyped, "; "),
			"A parameter's type is its name -- \"string\", \"map\" -- never a number. A "+
				"language that serialises an enum as an integer produces exactly this, and "+
				"the host refuses the whole signature: the extension then reports no "+
				"functions at all, which is several steps from the cause."))
	} else {
		out = append(out, passed(typedRule, typedTitle))
	}
	return out
}

// isJSONString reports whether a raw value is a JSON string.
func isJSONString(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return strings.HasPrefix(trimmed, `"`)
}
