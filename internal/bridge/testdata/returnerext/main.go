// Command returnerext is a returner extension used by the tests and the
// lab.
//
// It stands in for the sixteen destinations SPEC 20.3 marks Bridged —
// postgres, redis, kafka — each of which needs a driver a control plane
// does not link. What it does instead is append to a file, which is
// provable without a database.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/edlitmus/halite/internal/bridge"
)

func main() {
	bridge.Confine()

	ext := &bridge.Extension{
		Name:    "filedb",
		Version: "1.0.0",
		Kind:    "returner",
		Functions: []json.RawMessage{
			json.RawMessage(`{"module":"returner","function":"return","doc":"File one return.",` +
				`"mutates":true,"params":[{"name":"return","type":"map","required":true,"doc":"The return."}]}`),
			json.RawMessage(`{"module":"returner","function":"event","doc":"File one event.",` +
				`"mutates":true,"params":[{"name":"event","type":"map","required":true,"doc":"The event."}]}`),
		},
		Handler: handle,
	}
	if err := ext.Serve(); err != nil {
		fmt.Fprintln(os.Stderr, "returnerext:", err)
		os.Exit(1)
	}
}

func handle(call bridge.Call) (any, error) {
	var kwargs map[string]any
	if err := json.Unmarshal(call.Kwargs, &kwargs); err != nil {
		return nil, err
	}
	var payload any
	switch call.Function {
	case "return":
		payload = kwargs["return"]
	case "event":
		payload = kwargs["event"]
	default:
		return nil, fmt.Errorf("filedb has no function %q", call.Function)
	}

	path := os.Getenv("FILEDB_PATH")
	if path == "" {
		// The working directory is the only place the sandbox lets an
		// extension write, so that is where it goes by default.
		path = "returns.ndjson"
	}
	line, err := json.Marshal(map[string]any{"kind": call.Function, "payload": payload})
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return nil, err
	}
	call.Log("info", "filed a "+call.Function)
	return map[string]any{"filed": true, "path": path}, nil
}
