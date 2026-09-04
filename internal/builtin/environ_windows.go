//go:build windows

package builtin

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/winreg"
)

// The two keys Windows keeps the environment in. A session is built from
// the machine key and then the user key, so a variable in both is the
// user's — which is why the scope has to be said rather than guessed.
const (
	machineEnvironKey = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
	userEnvironKey    = `Environment`
)

// persistentEnviron returns the registry key the named scope lives in.
//
// The default is the user's, matching Salt's permanent=True, and because
// the machine key is the one a mistake is felt on every account of the
// node rather than one.
func persistentEnviron(scope string) (environStore, error) {
	switch strings.ToLower(scope) {
	case "", "user":
		return registryEnvironStore{hive: "HKCU", path: userEnvironKey}, nil
	case "machine", "system":
		return registryEnvironStore{hive: "HKLM", path: machineEnvironKey}, nil
	default:
		return nil, fmt.Errorf("scope must be machine or user, not %q", scope)
	}
}

type registryEnvironStore struct{ hive, path string }

func (s registryEnvironStore) Name() string { return s.hive + `\` + s.path }

func (s registryEnvironStore) Items() (*value.Map, error) {
	vals, err := winreg.ListValues(s.hive, s.path, winreg.Native)
	if err != nil {
		if errors.Is(err, winreg.ErrNotExist) {
			return value.NewMap(0), nil
		}
		return nil, err
	}
	out := value.NewMap(len(vals))
	for _, v := range vals {
		// A REG_MULTI_SZ or a REG_DWORD under this key is not something
		// the session builder turns into a variable, and reporting it as
		// one would make a state believe it had to change it. Only the
		// two string types are the environment.
		if v.Type != "sz" && v.Type != "expand_sz" {
			continue
		}
		s, _ := v.Data.(string)
		out.Set(v.Name, s)
	}
	return out, nil
}

func (s registryEnvironStore) Set(key, val string) error {
	if _, err := winreg.CreateKey(s.hive, s.path, winreg.Native); err != nil {
		return err
	}
	return winreg.SetValue(s.hive, s.path, key, s.typeFor(key, val), val, winreg.Native)
}

func (s registryEnvironStore) Unset(key string) error {
	err := winreg.DeleteValue(s.hive, s.path, key, winreg.Native)
	if err != nil && !errors.Is(err, winreg.ErrNotExist) {
		return err
	}
	return nil
}

// Flush is the broadcast, and is why it is not part of Set: it waits on
// every top-level window on the desktop, which on a busy node is most of
// a second, and a declaration naming six variables should pay that once.
func (s registryEnvironStore) Flush() { broadcastEnvironmentChange() }

// typeFor picks the registry type to write the value as.
//
// A value carrying %NAME% has to be REG_EXPAND_SZ or the session builder
// hands the literal percent signs to every process — PATH written as
// REG_SZ is the classic way to break a login. Where the value already
// exists its type is kept, because an operator who chose expand_sz for a
// value with nothing to expand chose it for a reason.
func (s registryEnvironStore) typeFor(key, val string) string {
	if cur, err := winreg.ReadValue(s.hive, s.path, key, winreg.Native); err == nil {
		if cur.Type == "sz" || cur.Type == "expand_sz" {
			if cur.Type == "sz" && strings.Count(val, "%") >= 2 {
				return "expand_sz"
			}
			return cur.Type
		}
	}
	if strings.Count(val, "%") >= 2 {
		return "expand_sz"
	}
	return "sz"
}

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procSendMessageTimeoutW = user32.NewProc("SendMessageTimeoutW")
)

const (
	hwndBroadcast    = 0xFFFF
	wmSettingChange  = 0x001A
	smtoAbortIfHung  = 0x0002
	settingChangeMax = 1000 // milliseconds
)

// broadcastEnvironmentChange tells the running desktop that the key
// changed.
//
// Without it the new value is in the registry and in nothing else:
// Explorer caches the environment it hands to everything it launches, so
// a variable set by a state is not seen by a program started from the
// Start menu until the next logon. The broadcast is best-effort — a
// service on a node with no interactive session has nothing to tell, and
// a window that does not answer within the timeout is skipped rather than
// held on to — so its failure is not the state's failure.
func broadcastEnvironmentChange() {
	p, err := windows.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	var result uintptr
	_, _, _ = procSendMessageTimeoutW.Call(
		uintptr(hwndBroadcast),
		uintptr(wmSettingChange),
		0,
		uintptr(unsafe.Pointer(p)),
		uintptr(smtoAbortIfHung),
		uintptr(settingChangeMax),
		uintptr(unsafe.Pointer(&result)),
	)
}
