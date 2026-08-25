// Command echoext is an extension used by the bridge's tests.
//
// It is a real executable rather than a mock of the protocol, because
// the property worth testing is that a separate process and this host
// understand each other — which a mock cannot establish.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/edlitmus/halite/internal/bridge"
)

func main() {
	// The limits the host asked for, applied to this process.
	bridge.Confine()

	ext := &bridge.Extension{
		Name:     "echo",
		Version:  "1.0.0",
		Kind:     "module",
		Declares: declaresFromEnv(),
		Functions: []json.RawMessage{
			json.RawMessage(`{"module":"echo","function":"say","doc":"Return what it was given."}`),
		},
		Handler: handle,
	}
	if err := ext.Serve(); err != nil {
		fmt.Fprintln(os.Stderr, "echoext:", err)
		os.Exit(1)
	}
}

func declaresFromEnv() []string {
	if os.Getenv("ECHOEXT_DECLARES") == "" {
		return nil
	}
	return []string{os.Getenv("ECHOEXT_DECLARES")}
}

func handle(call bridge.Call) (any, error) {
	switch call.Function {
	case "say":
		var kwargs map[string]any
		_ = json.Unmarshal(call.Kwargs, &kwargs)
		return map[string]any{
			"said":    kwargs["message"],
			"node_id": call.Context.NodeID,
			"test":    call.Context.Test,
		}, nil

	case "stream":
		call.Log("info", "starting")
		call.Progress(1, 2, "half")
		call.Event("halite/ext/echo", map[string]any{"seen": true})
		call.Log("warn", "nearly done")
		return "streamed", nil

	case "fail":
		return nil, fmt.Errorf("this function always fails")

	case "panic":
		panic("this function always panics")

	case "sleep":
		var kwargs map[string]any
		_ = json.Unmarshal(call.Kwargs, &kwargs)
		seconds, _ := kwargs["seconds"].(float64)
		time.Sleep(time.Duration(seconds * float64(time.Second)))
		return "awake", nil

	case "environment":
		return map[string]any{
			"vars":           os.Environ(),
			"network_denied": bridge.NetworkDenied(),
		}, nil

	case "limits":
		return map[string]any{"nofile": readLimit()}, nil

	case "garbage":
		// Writes something that is not a frame, to prove the host
		// treats a protocol violation as fatal to the process.
		os.Stdout.WriteString("this is not a frame")
		time.Sleep(5 * time.Second)
		return nil, nil

	case "exit":
		os.Exit(3)
	}
	return nil, fmt.Errorf("echo has no function %q", call.Function)
}

// readLimit reports the open-file limit this process is running under,
// so a test can see whether Confine took effect.
func readLimit() string {
	return strconv.FormatUint(currentNoFile(), 10)
}
