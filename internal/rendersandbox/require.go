package rendersandbox

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
)

// The controls `render_sandbox_require` can name. Issue 762, DIVERGENCE
// 5.696.
//
// A list rather than one switch, because what can be applied differs by
// platform and an operator's honest requirement differs with it: a
// FreeBSD node can drop the child to an unprivileged account and has no
// network namespace this build can use, so "require everything" there
// would be a node that never renders, and "require nothing" would give up
// the identity it can have. Filesystem restriction is not a name here
// because it is built on no platform; a requirement that can only ever
// refuse is a setting that cannot be used, and saying so in Describe is
// the honest place for it.
const (
	RequireIdentity = "identity"
	RequireNetwork  = "network"
)

// ParseRequirements checks a `render_sandbox_require` list, and returns it
// sorted with duplicates removed. A name it does not know is an error,
// because a misspelt requirement that was quietly dropped is a control
// that reports itself as required and is not.
func ParseRequirements(names []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		switch name {
		case RequireIdentity, RequireNetwork:
		default:
			return nil, fmt.Errorf("render_sandbox_require names %q, which is not a control the render sandbox has; "+
				"it takes %s and %s", name, RequireIdentity, RequireNetwork)
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Observed is what the child reports about itself on its hello: measured
// in the child, after every control was applied, rather than inferred in
// the parent from what it meant to apply.
//
// The difference is the point. Describe says what this process asked the
// kernel for, which is right as a description and is a claim. A required
// control is checked against this instead, so a requirement is met only
// when the child that will do the rendering is seen to be confined.
type Observed struct {
	UID int `json:"uid"`
	GID int `json:"gid"`
	// UpInterfaces are the network interfaces the child sees up. A child
	// in a network namespace of its own sees none, not even loopback.
	UpInterfaces []string `json:"up_interfaces,omitempty"`
	// NetworkError is why the interfaces could not be listed. It is not
	// read as "no network": a check that cannot look has not seen
	// nothing.
	NetworkError string `json:"network_error,omitempty"`
}

// observeSelf is the child's measurement of itself.
//
// Listing interfaces opens a routing socket on most platforms, the one
// socket the child opens; it binds nothing and connects to nothing.
func observeSelf() *Observed {
	o := &Observed{UID: os.Getuid(), GID: os.Getgid()}
	ifaces, err := net.Interfaces()
	if err != nil {
		o.NetworkError = err.Error()
		return o
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp != 0 {
			o.UpInterfaces = append(o.UpInterfaces, iface.Name)
		}
	}
	return o
}

// ErrUnmet marks a render refused because a required control was not in
// force. Matched with errors.Is.
var ErrUnmet = errors.New("a required render isolation control is not in force")

// Observe starts the child if none is running and returns what it
// reported about itself.
func (s *Sandbox) Observe() (Observed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.child == nil {
		c, err := start(s.cfg)
		if err != nil {
			return Observed{}, err
		}
		s.child = c
	}
	return s.child.observed, nil
}

// Required is the configured requirement list.
func (s *Sandbox) Required() []string { return append([]string(nil), s.cfg.Require...) }

// Unmet reports each required control the running child is not under, in
// words that say what was seen. Empty when nothing is required or all of
// it holds; an error when no child could be started to look at.
func (s *Sandbox) Unmet() ([]string, error) {
	o, err := s.Observe()
	if err != nil {
		return nil, err
	}
	return unmet(s.cfg, o), nil
}

func unmet(cfg Config, o Observed) []string {
	var out []string
	for _, name := range cfg.Require {
		switch name {
		case RequireIdentity:
			if reason := identityUnmet(cfg, o); reason != "" {
				out = append(out, reason)
			}
		case RequireNetwork:
			if o.NetworkError != "" {
				out = append(out, "network is required to be denied, and the child could not list its interfaces to show it is: "+o.NetworkError)
			} else if len(o.UpInterfaces) > 0 {
				out = append(out, "network is required to be denied, and the child sees "+
					strings.Join(o.UpInterfaces, ", ")+" up; "+networkEnforcement())
			}
		}
	}
	return out
}

func identityUnmet(cfg Config, o Observed) string {
	if cfg.User == "" {
		return "identity is required, and no render_sandbox_user is configured for the child to run as"
	}
	uid, _, err := renderAccount(cfg)
	if err != nil {
		return "identity is required, and " + err.Error()
	}
	if o.UID != int(uid) {
		reason := fmt.Sprintf("identity is required, and the child runs as uid %d rather than as %s (uid %d)", o.UID, cfg.User, uid)
		if !canDropPrivilege() {
			reason += ", because this process cannot drop privilege here"
		}
		return reason
	}
	return ""
}

// refuseUnmet is renderInChild's gate: a child that does not meet the
// requirement is closed rather than used, and every render says why.
//
// Not cached. The next render starts a child and looks again, so a node
// whose account was created, or that was restarted as root, renders
// without a restart of its own. A refusal stored from one moment would
// turn a condition somebody fixed into one that lasts until a restart.
func refuseUnmet(cfg Config, c *childProcess, file string) error {
	if len(cfg.Require) == 0 {
		return nil
	}
	reasons := unmet(cfg, c.observed)
	if len(reasons) == 0 {
		return nil
	}
	return fmt.Errorf("%s: not rendered, because render_sandbox_require is not met: %s: %w",
		file, strings.Join(reasons, "; "), ErrUnmet)
}
