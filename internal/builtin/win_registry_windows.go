package builtin

import (
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/winreg"
)

// The Windows half of win_registry. internal/winreg holds the registry
// work; this is the translation between it and a module's arguments and
// return values.

func winRegView(view string) (winreg.View, error) { return winreg.ParseView(view) }

func winRegReadValue(hive, key, vname, view string) (any, error) {
	v, err := winRegView(view)
	if err != nil {
		return nil, err
	}
	got, err := winreg.ReadValue(hive, key, vname, v)
	if err != nil {
		return nil, err
	}
	return valueMap(got), nil
}

func valueMap(v winreg.Value) *value.Map {
	return value.MapOf("name", v.Name, "type", v.Type, "data", v.Data)
}

func winRegSetValue(hive, key, vname, vtype string, data any, view string) error {
	v, err := winRegView(view)
	if err != nil {
		return err
	}
	return winreg.SetValue(hive, key, vname, vtype, data, v)
}

func winRegDeleteValue(hive, key, vname, view string) error {
	v, err := winRegView(view)
	if err != nil {
		return err
	}
	return winreg.DeleteValue(hive, key, vname, v)
}

func winRegListKeys(hive, key, view string) (any, error) {
	v, err := winRegView(view)
	if err != nil {
		return nil, err
	}
	names, err := winreg.ListKeys(hive, key, v)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(names))
	for _, n := range names {
		out = append(out, n)
	}
	return out, nil
}

func winRegListValues(hive, key, view string) (any, error) {
	v, err := winRegView(view)
	if err != nil {
		return nil, err
	}
	values, err := winreg.ListValues(hive, key, v)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(values))
	for _, item := range values {
		out = append(out, valueMap(item))
	}
	return out, nil
}

func winRegKeyExists(hive, key, view string) (bool, error) {
	v, err := winRegView(view)
	if err != nil {
		return false, err
	}
	return winreg.KeyExists(hive, key, v)
}

func winRegValueExists(hive, key, vname, view string) (bool, error) {
	v, err := winRegView(view)
	if err != nil {
		return false, err
	}
	return winreg.ValueExists(hive, key, vname, v)
}

func winRegCreateKey(hive, key, view string) (any, error) {
	v, err := winRegView(view)
	if err != nil {
		return nil, err
	}
	created, err := winreg.CreateKey(hive, key, v)
	if err != nil {
		return nil, err
	}
	return created, nil
}

func winRegDeleteKey(hive, key string, recursive bool, view string) error {
	v, err := winRegView(view)
	if err != nil {
		return err
	}
	if recursive {
		return winreg.DeleteKeyRecursive(hive, key, v)
	}
	return winreg.DeleteKey(hive, key, v)
}
