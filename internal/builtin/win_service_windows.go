package builtin

import (
	"fmt"

	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/winsvc"
)

// The Windows half of win_service. internal/winsvc holds the service
// control manager client; this is the translation between it and a
// module's arguments and return values.

func winServiceInfo(name string) (any, error) {
	if name == "" {
		return nil, fmt.Errorf("win_service.info needs a service name")
	}
	s, err := winsvc.Query(name)
	if err != nil {
		return nil, err
	}
	return value.MapOf(
		"name", s.Name,
		"display_name", s.DisplayName,
		"state", s.State,
		"start_type", s.StartType,
		"pid", int64(s.PID),
		"binary_path", s.BinaryPath,
		"account", s.Account,
		"description", s.Description,
	), nil
}

func winServiceStartType(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("win_service.get_start_type needs a service name")
	}
	s, err := winsvc.Query(name)
	if err != nil {
		return "", err
	}
	return s.StartType, nil
}

func winServiceSetStartType(name, startType string) error {
	if name == "" {
		return fmt.Errorf("win_service.set_start_type needs a service name")
	}
	return winsvc.SetStartType(name, startType)
}

func winServiceList() (any, error) {
	all, err := winsvc.ListStatus()
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(all))
	for _, s := range all {
		out = append(out, value.MapOf(
			"name", s.Name,
			"display_name", s.DisplayName,
			"state", s.State,
			"pid", int64(s.PID),
		))
	}
	return out, nil
}
