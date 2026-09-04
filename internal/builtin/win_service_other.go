//go:build !windows

package builtin

import "fmt"

// win_service off Windows.
//
// The signatures declare `platforms: windows`, so a call is refused
// before it reaches any of this and the operator is told which platform
// the function runs on. These exist so the module registers everywhere,
// which is what keeps "not written yet" and "you have mistyped it"
// different messages.

func notWindowsService(function string) error {
	return fmt.Errorf("win_service.%s speaks to the Windows service control manager, "+
		"and this node is not Windows", function)
}

func winServiceInfo(name string) (any, error) { return nil, notWindowsService("info") }

func winServiceStartType(name string) (string, error) {
	return "", notWindowsService("get_start_type")
}

func winServiceSetStartType(name, startType string) error {
	return notWindowsService("set_start_type")
}

func winServiceList() (any, error) { return nil, notWindowsService("list") }
