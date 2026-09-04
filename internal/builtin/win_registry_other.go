//go:build !windows

package builtin

import "fmt"

// win_registry off Windows.
//
// The signatures declare `platforms: windows`, so a call is refused
// before it reaches any of this. These exist so the module registers
// everywhere, which is what keeps "not written yet" and "you have
// mistyped it" different messages.

func notWindowsRegistry(function string) error {
	return fmt.Errorf("win_registry.%s reads the Windows registry, "+
		"and this node is not Windows", function)
}

func winRegReadValue(hive, key, vname, view string) (any, error) {
	return nil, notWindowsRegistry("read_value")
}

func winRegSetValue(hive, key, vname, vtype string, data any, view string) error {
	return notWindowsRegistry("set_value")
}

func winRegDeleteValue(hive, key, vname, view string) error {
	return notWindowsRegistry("delete_value")
}

func winRegListKeys(hive, key, view string) (any, error) {
	return nil, notWindowsRegistry("list_keys")
}

func winRegListValues(hive, key, view string) (any, error) {
	return nil, notWindowsRegistry("list_values")
}

func winRegKeyExists(hive, key, view string) (bool, error) {
	return false, notWindowsRegistry("key_exists")
}

func winRegValueExists(hive, key, vname, view string) (bool, error) {
	return false, notWindowsRegistry("value_exists")
}

func winRegCreateKey(hive, key, view string) (any, error) {
	return nil, notWindowsRegistry("create_key")
}

func winRegDeleteKey(hive, key string, recursive bool, view string) error {
	return notWindowsRegistry("delete_key")
}
