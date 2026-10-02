// Command badext breaks one protocol rule at a time.
//
// A conformance harness that has only ever been run against conforming
// extensions establishes nothing: every check would pass with the body
// deleted. So this one implements the wire by hand -- deliberately, not
// through `ext`, because the package exists to make these mistakes
// impossible -- and misbehaves in whichever way BADEXT_MODE names.
//
// Writing it by hand has a second use. It is a third independent
// implementation of the protocol, after the Go package and the Python
// example, and a rule that only the two cooperating implementations
// agree on is a rule the specification does not really carry.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// mode selects the misbehaviour.
const (
	modeGood          = ""                  // conforms, so the harness has a baseline
	modeNoName        = "no-name"           // hello_ok with no name
	modeNoVersion     = "no-version"        // hello_ok with no version
	modeNoFunctions   = "no-functions"      // announces nothing
	modeNumericType   = "numeric-type"      // a parameter type as an integer
	modeUnnamedSig    = "unnamed-signature" // a signature naming neither
	modeNoResult      = "no-result"         // a call answered with silence
	modeTwoResults    = "two-results"       // a call answered twice
	modeWrongID       = "wrong-id"          // a result carrying another id
	modeAnyProtocol   = "any-protocol"      // answers a version it cannot know
	modeAnyKind       = "any-kind"          // answers for a kind it is not
	modeIgnoreShutdwn = "ignores-shutdown"  // keeps running
	modeUnknownIsOK   = "unknown-is-ok"     // succeeds at a function it lacks
	modePrintsToStdou = "prints-to-stdout"  // a print() into the protocol
	modeStrictFields  = "strict-fields"     // refuses a hello field it does not know
	modeSlowStart     = "slow-start"        // conforms, after a start slower than one exchange
	modeBuffers       = "buffers"           // holds every frame until it exits
)

// slowStart is how long modeSlowStart takes before it reads anything.
//
// Longer than the two seconds the tests give one exchange, so that a
// harness timing the first answer against the exchange timeout fails
// it every time; much shorter than the start allowance, so that one
// timing it against the allowance passes it every time. A load spike
// does the same thing to a conforming extension, but only sometimes,
// which is how this was found and why it is reproduced like this.
const slowStart = 3 * time.Second

// helloFields are the fields a hello is defined to carry. Used only by
// modeStrictFields, to be wrong in the way SPEC 24.7 forbids.
var helloFields = map[string]bool{"kind": true, "protocol": true, "extension_kind": true}

// modes is every misbehaviour this understands.
//
// An unknown one is fatal rather than ignored. A test naming a mode
// that has been renamed would otherwise get the conforming behaviour
// and pass -- a check that has quietly stopped checking, which is the
// failure this whole package exists to catch in other people's code.
var modes = map[string]bool{
	modeGood: true, modeNoName: true, modeNoVersion: true, modeNoFunctions: true,
	modeNumericType: true, modeUnnamedSig: true, modeNoResult: true,
	modeTwoResults: true, modeWrongID: true, modeAnyProtocol: true,
	modeAnyKind: true, modeIgnoreShutdwn: true, modeUnknownIsOK: true,
	modePrintsToStdou: true, modeStrictFields: true, modeSlowStart: true,
	modeBuffers: true,
}

// out is where frames go. Unbuffered, except in modeBuffers.
var out io.Writer = os.Stdout

func main() {
	mode := os.Getenv("BADEXT_MODE")
	if !modes[mode] {
		fmt.Fprintf(os.Stderr, "badext: %q is not a mode it knows\n", mode)
		os.Exit(2)
	}

	if mode == modeSlowStart {
		// What a loaded machine, a cold interpreter or a first-exec
		// signature check does to a process before main() has done
		// anything at all. The harness cannot see the difference, and
		// is not meant to: it is meant to not blame the extension.
		time.Sleep(slowStart)
	}
	if mode == modeBuffers {
		// The writer a language with buffered stdout gives you by
		// default: everything is held, and released only when the
		// process ends. Flushed on every way out of main, because
		// that is what a runtime does at exit -- and why the frames
		// arrive, too late, once the host gives up and closes stdin.
		buffered := bufio.NewWriterSize(os.Stdout, 1<<16)
		out = buffered
		defer buffered.Flush()
	}

	if mode == modePrintsToStdou {
		// The single most common way to break this, and it breaks the
		// stream before a frame is ever read.
		fmt.Println("starting up")
	}

	if err := serve(mode); err != nil {
		fmt.Fprintln(os.Stderr, "badext:", err)
		if flusher, ok := out.(*bufio.Writer); ok {
			_ = flusher.Flush()
		}
		os.Exit(1)
	}
}

// serve is the session, returning rather than exiting so that the
// buffered mode's deferred flush runs on every path that a real
// runtime's exit would flush.
func serve(mode string) error {
	hello, err := readFrame()
	if err != nil {
		return err
	}
	if kind, _ := hello["kind"].(string); kind != "hello" {
		return fmt.Errorf("the host opened with %s", kind)
	}
	if mode != modeAnyProtocol {
		if protocol, _ := hello["protocol"].(float64); int(protocol) != 1 {
			return fmt.Errorf("this speaks protocol 1")
		}
	}
	if mode != modeAnyKind {
		if wanted, _ := hello["extension_kind"].(string); wanted != "" && wanted != "module" {
			return fmt.Errorf("this is a module extension")
		}
	}
	if mode == modeStrictFields {
		// What a strict decoder does, which is a reasonable instinct
		// and the one place it is wrong: SPEC 24.7 makes tolerance of
		// an unknown field the basis of every additive change.
		for field := range hello {
			if !helloFields[field] {
				return fmt.Errorf("%q is not a field of hello", field)
			}
		}
	}

	writeFrame(helloOK(mode))

	for {
		frame, err := readFrame()
		if err != nil {
			return nil
		}
		kind, _ := frame["kind"].(string)
		switch kind {
		case "shutdown":
			if mode == modeIgnoreShutdwn {
				continue
			}
			return nil
		case "call":
			answer(mode, frame)
		default:
			return nil
		}
	}
}

func helloOK(mode string) map[string]any {
	frame := map[string]any{
		"kind": "hello_ok", "name": "badext", "version": "1.0.0",
		"functions": []any{map[string]any{
			"module": "badext", "function": "say",
			"params": []any{map[string]any{
				"name": "message", "type": "string", "required": true,
			}},
		}},
	}
	switch mode {
	case modeNoName:
		delete(frame, "name")
	case modeNoVersion:
		delete(frame, "version")
	case modeNoFunctions:
		delete(frame, "functions")
	case modeNumericType:
		frame["functions"] = []any{map[string]any{
			"module": "badext", "function": "say",
			// What an enum serialises to, which is the mistake this
			// rule exists to catch.
			"params": []any{map[string]any{"name": "message", "type": 1}},
		}}
	case modeUnnamedSig:
		frame["functions"] = []any{map[string]any{"doc": "it names neither"}}
	}
	return frame
}

func answer(mode string, frame map[string]any) {
	id, _ := frame["id"].(string)
	function, _ := frame["function"].(string)

	if mode == modeNoResult {
		return
	}
	if mode == modeWrongID {
		id = "not-the-id-you-sent"
	}

	if function != "say" && mode != modeUnknownIsOK {
		writeFrame(map[string]any{
			"kind": "result", "id": id,
			"error": "badext provides say(), not " + function,
		})
		return
	}

	writeFrame(map[string]any{"kind": "log", "id": id, "level": "debug", "message": "answering"})
	writeFrame(map[string]any{"kind": "result", "id": id, "ok": true, "value": "hi"})
	if mode == modeTwoResults {
		writeFrame(map[string]any{"kind": "result", "id": id, "ok": true, "value": "hi again"})
	}
}

// readFrame takes four bytes of big-endian length and that many bytes
// of JSON.
func readFrame() (map[string]any, error) {
	var header [4]byte
	if _, err := io.ReadFull(os.Stdin, header[:]); err != nil {
		return nil, err
	}
	size := int(header[0])<<24 | int(header[1])<<16 | int(header[2])<<8 | int(header[3])
	body := make([]byte, size)
	if _, err := io.ReadFull(os.Stdin, body); err != nil {
		return nil, err
	}
	var frame map[string]any
	if err := json.Unmarshal(body, &frame); err != nil {
		return nil, err
	}
	return frame, nil
}

func writeFrame(frame map[string]any) {
	body, err := json.Marshal(frame)
	if err != nil {
		return
	}
	header := [4]byte{
		byte(len(body) >> 24), byte(len(body) >> 16),
		byte(len(body) >> 8), byte(len(body)),
	}
	_, _ = out.Write(header[:])
	_, _ = out.Write(body)
	// os.Stdout is unbuffered in Go, so there is nothing to flush here
	// -- except in modeBuffers, which is the point of modeBuffers. An
	// implementation in a language whose stdout buffers must flush, and
	// one that does not looks to the host exactly like a hang.
}
