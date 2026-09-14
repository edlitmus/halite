package extconform

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/edlitmus/halite/ext"
)

// The rules that need a call, a shutdown, or a session that begins
// wrongly.

// noSuchFunction is a name no extension can plausibly provide.
const noSuchFunction = "__halite_conformance_no_such_function__"

// checkCalls exercises the call path.
func checkCalls(s *session, opts Options, hello *looseFrame) []Result {
	const (
		oneResultRule = "call/answers-with-one-result"
		idRule        = "call/the-result-carries-the-call-id"
		unknownRule   = "call/an-unknown-function-is-an-error"
		survivesRule  = "call/a-failed-call-leaves-it-usable"
	)
	oneResultTitle := "a call is answered with exactly one result"
	idTitle := "the result carries the id of the call"
	unknownTitle := "an unknown function is an error, not a crash"
	survivesTitle := "a failed call leaves it able to answer the next"

	function := opts.Function
	if function == "" && len(hello.Functions) > 0 {
		function = hello.Functions[0].Function
	}
	if function == "" {
		reason := "the extension announced no function to call, and none was named"
		return []Result{
			skipped(oneResultRule, oneResultTitle, reason),
			skipped(idRule, idTitle, reason),
			skipped(unknownRule, unknownTitle, reason),
			skipped(survivesRule, survivesTitle, reason),
		}
	}

	var out []Result

	// A real call. Whether it succeeds is the extension's business --
	// a call with no arguments may legitimately fail. What is being
	// checked is the shape of the answer.
	if err := s.write(ext.Frame{
		Kind: ext.FrameCall, ID: "c1", Function: function,
		Context: &ext.CallContext{NodeID: "conformance", Env: "base"},
		Kwargs:  encodeOrNull(opts.Kwargs),
	}); err != nil {
		reason := "the call could not be written: " + err.Error()
		return []Result{
			failed(oneResultRule, oneResultTitle, reason, whyOneResult),
			skipped(idRule, idTitle, reason),
			skipped(unknownRule, unknownTitle, reason),
			skipped(survivesRule, survivesTitle, reason),
		}
	}

	result, streamed, err := s.drainUntilResult()
	if err != nil {
		reason := describeReadFailure(err, s)
		return []Result{
			failed(oneResultRule, oneResultTitle,
				fmt.Sprintf("calling %s(): %s", function, reason), whyOneResult),
			skipped(idRule, idTitle, "no result arrived"),
			skipped(unknownRule, unknownTitle, "the first call did not complete"),
			skipped(survivesRule, survivesTitle, "the first call did not complete"),
		}
	}
	out = append(out, passed(oneResultRule, oneResultTitle))

	if result.ID != "c1" {
		out = append(out, failed(idRule, idTitle,
			fmt.Sprintf("the call carried id %q and the result carried %q", "c1", result.ID),
			"A host runs several calls over one process. The id is how an answer is matched "+
				"to its question, and one that does not come back matches the wrong call or "+
				"none at all."))
	} else {
		out = append(out, passed(idRule, idTitle))
	}

	// The streaming frames carry it too, and a host that logs them
	// against the wrong call is worse than one that drops them.
	for _, frame := range streamed {
		if frame.ID != "c1" {
			out = append(out, failed("call/streaming-frames-carry-the-call-id",
				"a log, progress or event frame carries the id of its call",
				fmt.Sprintf("a %s frame carried id %q", frame.Kind, frame.ID),
				"The host records these against the call they belong to. One with the wrong "+
					"id is attributed to another call, or to none."))
			break
		}
	}

	// A function that cannot exist. This is the check that most often
	// finds a handler which falls off the end without answering.
	if err := s.write(ext.Frame{
		Kind: ext.FrameCall, ID: "c2", Function: noSuchFunction,
	}); err != nil {
		out = append(out, failed(unknownRule, unknownTitle,
			"the call could not be written: "+err.Error(), whyUnknown))
		return append(out, skipped(survivesRule, survivesTitle, "the second call was not made"))
	}
	unknown, _, err := s.drainUntilResult()
	switch {
	case err != nil:
		out = append(out, failed(unknownRule, unknownTitle,
			describeReadFailure(err, s), whyUnknown))
		return append(out, skipped(survivesRule, survivesTitle,
			"the extension did not answer the unknown function"))
	case unknown.OK:
		out = append(out, failed(unknownRule, unknownTitle,
			fmt.Sprintf("it answered %s() successfully", noSuchFunction),
			"An extension that succeeds at a function it does not have will succeed at a "+
				"misspelt one too, and a state calling the wrong name gets a result instead "+
				"of an error."))
	case unknown.Error == "":
		out = append(out, failed(unknownRule, unknownTitle,
			"it answered with a failure and no message", whyUnknown))
	default:
		out = append(out, passed(unknownRule, unknownTitle))
	}

	// And still usable. A process the host must restart after every
	// failed call is a process the pool cannot keep.
	if err := s.write(ext.Frame{
		Kind: ext.FrameCall, ID: "c3", Function: function,
		Kwargs: encodeOrNull(opts.Kwargs),
	}); err != nil {
		return append(out, failed(survivesRule, survivesTitle,
			"the call could not be written: "+err.Error(), whySurvives))
	}
	again, _, err := s.drainUntilResult()
	switch {
	case err != nil:
		out = append(out, failed(survivesRule, survivesTitle,
			describeReadFailure(err, s), whySurvives))
	case again.ID != "c3":
		out = append(out, failed(survivesRule, survivesTitle,
			fmt.Sprintf("the third call carried id %q and the answer carried %q", "c3", again.ID),
			"An answer to an earlier call arriving here means the extension has lost its "+
				"place in the stream, which is what a missing or a doubled result frame does."))
	default:
		out = append(out, passed(survivesRule, survivesTitle))
	}
	return out
}

// checkShutdown asks it to stop.
func checkShutdown(s *session, opts Options) Result {
	const rule = "lifecycle/shutdown-ends-it"
	title := "a shutdown frame ends the process"
	why := "Shutdown is a courtesy -- the host kills a process that ignores it -- but one " +
		"that takes the kill every time cannot flush what it was holding, and every stop " +
		"of the agent waits out a timeout it did not need to."

	if err := s.write(ext.Frame{Kind: ext.FrameShutdown}); err != nil {
		return failed(rule, title, "the shutdown frame could not be written: "+err.Error(), why)
	}
	// Generous relative to the exchanges above: an extension may have
	// something to flush, which is the whole point of asking.
	if !s.waitForExit(opts.timeout()) {
		return failed(rule, title,
			"it was still running when the timeout expired"+tail(s), why)
	}
	return passed(rule, title)
}

// checkProtocolRefusal opens a session claiming a version nothing
// speaks.
func checkProtocolRefusal(ctx context.Context, opts Options) Result {
	const rule = "protocol/refuses-another-version"
	title := "it refuses a protocol version it does not speak"
	why := "The version is a single integer and there is no negotiation: an extension " +
		"either speaks this protocol or it does not. One that answers a version it has " +
		"never seen is agreeing to a contract it cannot have read, and the disagreement " +
		"surfaces later as a field that is missing or means something else."

	s, err := start(ctx, opts)
	if err != nil {
		return skipped(rule, title, "it could not be started again: "+err.Error())
	}
	defer s.close()

	if err := s.write(ext.Frame{
		Kind: ext.FrameHello, Protocol: ext.ProtocolVersion + 1, Ext: opts.Kind,
	}); err != nil {
		return skipped(rule, title, "the hello frame could not be written: "+err.Error())
	}
	frame, readErr := s.read()
	if readErr != nil {
		// Exiting, or saying nothing, is a refusal.
		return passed(rule, title)
	}
	if frame.Kind == ext.FrameHelloOK {
		return failed(rule, title,
			fmt.Sprintf("it answered hello_ok to protocol %d", ext.ProtocolVersion+1), why)
	}
	return passed(rule, title)
}

// checkKindRefusal opens a session asking for a kind the extension does
// not provide.
func checkKindRefusal(ctx context.Context, opts Options) Result {
	const rule = "protocol/refuses-another-kind"
	title := "it refuses a kind it does not provide"
	why := "The host names the kind it wants, and an extension that answers to any of them " +
		"will be used as one it is not: a returner asked for pillar fails somewhere much " +
		"later, with a message about the shape of its answer rather than about the mistake."

	if opts.Kind == "" {
		return skipped(rule, title,
			"no --kind was given, so there is no kind to ask for other than the right one")
	}
	other := ext.KindReturner
	if opts.Kind == other {
		other = ext.KindPillar
	}

	s, err := start(ctx, opts)
	if err != nil {
		return skipped(rule, title, "it could not be started again: "+err.Error())
	}
	defer s.close()

	if err := s.write(ext.Frame{
		Kind: ext.FrameHello, Protocol: ext.ProtocolVersion, Ext: other,
	}); err != nil {
		return skipped(rule, title, "the hello frame could not be written: "+err.Error())
	}
	frame, readErr := s.read()
	if readErr != nil {
		return passed(rule, title)
	}
	if frame.Kind == ext.FrameHelloOK {
		return failed(rule, title,
			fmt.Sprintf("it answered hello_ok when the host asked for a %q extension "+
				"and this one is %q", other, opts.Kind), why)
	}
	return passed(rule, title)
}

func tail(s *session) string {
	if said := s.stderrTail(); said != "" {
		return "; it wrote to stderr: " + said
	}
	return ""
}

// encodeOrNull renders the kwargs, or nothing.
func encodeOrNull(v any) []byte {
	if v == nil {
		return nil
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return encoded
}

const (
	whyOneResult = "Exactly one result, whatever happens. A handler that falls off the end " +
		"without answering leaves the host waiting until it kills the process on the " +
		"timeout -- reported as a hang rather than as the error it was. Two results put " +
		"the stream out of step, and every later answer goes to the wrong call."
	whyUnknown = "A call names a function by string, and a host will eventually send one " +
		"this extension does not have -- a misspelling in a state, or a version that " +
		"dropped it. The answer is an error result. Exiting makes the host restart the " +
		"process; hanging makes it wait out the timeout."
	whySurvives = "The host keeps a pool of processes and reuses them. One that has to be " +
		"restarted after every failed call turns an ordinary error into a process churn, " +
		"and the concurrency the pool exists for is gone."
)
