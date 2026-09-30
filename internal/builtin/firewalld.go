package builtin

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerFirewalld installs the `firewalld` module of SPEC 15.3's RHEL
// row and the `firewalld.present` state of SPEC 15.5, driving
// `firewall-cmd` against the running daemon.
//
// # Why this is not a `firewall` provider
//
// firewalld is the RHEL family's own front end in the way ufw is
// Ubuntu's, so the obvious move is to make it `firewall`'s third
// provider. It is not, for a reason that is about evidence rather than
// shape. `firewall.allowed` means "open this port on this host", and
// in firewalld that can only mean the *default zone* — the zone the
// primary interface is bound to, and on every host this module was
// built against, the zone carrying the SSH session the work arrived
// over. A provider would therefore be demonstrated by changing the one
// zone the lab could not safely change, or not demonstrated at all.
// Everything below was driven against a throwaway zone with no
// interface bound to it, which is how this module could be shown
// working without putting the hosts at risk; a provider built on top of
// it would be exercising a path nobody watched. It would also have to
// answer questions `firewallProvider` has no words for — which zone,
// runtime or permanent — and that interface was deliberately left
// shaped by ufw until a second real provider reshapes it (see
// firewall.go). So `firewall` still refuses on a firewalld host, and
// says to use this module. The relationship is `iptables` to
// `firewall`: the layer a tree reaches for directly.
//
// # Membership is asked of firewalld, never read from a listing
//
// firewalld normalises what it is given. A rich rule added as
//
//	rule family=ipv4 source address=192.0.2.0/24 service name=ssh accept
//
// is listed back as
//
//	rule family="ipv4" source address="192.0.2.0/24" service name="ssh" accept
//
// on both 0.9.11 and 1.3.4, so a state that compared the text it was
// given against `--list-rich-rules` would never find its own rule and
// would add it on every run. That is DIVERGENCE 5.31's trap in `pf`,
// in firewalld's spelling. So every "is it there?" goes through
// firewall-cmd's own `--query-*`, which parses the operand and compares
// it the way firewalld does: exit 0 and "yes", or exit 1 and "no".
//
// The query is semantic, which has one consequence worth knowing:
// `--query-port=9000/udp` answers yes when `9000-9001/udp` is open,
// because the range covers it. Without pruning that is the right
// answer — the port is open. With pruning it is not, because the range
// is about to be removed; so a pruning state decides membership from
// the listing instead, which is sound for services, ports and sources
// because firewalld echoes those back exactly as they were given
// (captured: `198.51.100.7/24` is stored as written, not as its
// network). Rich rules are not echoed, which is why there is no
// `prune_rich_rules`.
//
// # Permanent by default, as Salt's is
//
// Every function takes `permanent`, defaulting to true, which is Salt's
// default: the change is written to /etc/firewalld and survives a
// restart, and the *running* firewall does not see it until a reload.
// `permanent: false` changes the running firewall only. `new_zone` and
// `delete_zone` are permanent-only in firewalld itself (the runtime has
// no way to create a zone), so they reload afterwards unless told not
// to, again as Salt's do, and `firewalld.present` reloads when it
// changed anything. A reload discards runtime-only changes on every
// zone, not just the one this touched — true of Salt's module too, and
// said here because it is the thing an operator would want to know.
func registerFirewalld(r *Registries) {
	r.Exec.Add(firewalldExecModules()...)
	r.States.Add(firewalldStateModule())
}

// firewalldExecModules and firewalldStateModule are separate from the
// registration so that the unit tests can bind arguments through the
// real signatures on a machine that is not Linux, where the registry
// would refuse before binding anything.
func firewalldExecModules() []exec.Module {
	permanent := opt("permanent", signature.Bool, true,
		"Act on the permanent configuration (the default, as in Salt) rather than the running firewall.")
	zoneOptional := opt("zone", signature.String, "", "The zone. Empty means the default zone.")
	zoneRequired := req("zone", signature.String, "The zone.")
	restart := opt("restart", signature.Bool, true,
		"Reload firewalld afterwards so the running firewall sees the change, as Salt does.")

	read := func(function, doc string, params []signature.Param, fn exec.Func) exec.Module {
		return exec.Module{
			Sig: signature.Signature{
				Module: "firewalld", Function: function, Doc: doc, Params: params,
				TestMode: signature.TestNotApplicable, Platforms: linuxOnly, Section: "15.3",
			},
			Fn: fn,
		}
	}
	mutate := func(function, doc string, params []signature.Param, fn exec.Func) exec.Module {
		return exec.Module{
			Sig: signature.Signature{
				Module: "firewalld", Function: function, Doc: doc, Params: params,
				Mutates: true, TestMode: signature.TestReliable, Privileges: []string{"root"},
				Platforms: linuxOnly, Section: "15.3",
			},
			Fn: fn,
		}
	}

	return []exec.Module{
		read("version", "Return firewalld's version.", nil, firewalldVersionFn),
		read("default_zone", "Return the default zone.", nil, firewalldDefaultZoneFn),
		read("get_zones", "Return the names of every zone.",
			[]signature.Param{permanent}, firewalldListFn("--get-zones", false)),
		read("get_services", "Return the names of every service firewalld knows, such as `http` or `ssh`.",
			[]signature.Param{permanent}, firewalldListFn("--get-services", false)),
		read("list_services", "Return the services a zone allows.",
			[]signature.Param{zoneOptional, permanent}, firewalldListFn("--list-services", true)),
		read("list_ports", "Return the ports a zone allows, as `port/protocol` or `from-to/protocol`.",
			[]signature.Param{zoneRequired, permanent}, firewalldListFn("--list-ports", true)),
		read("get_sources", "Return the source addresses and networks bound to a zone.",
			[]signature.Param{zoneRequired, permanent}, firewalldListFn("--list-sources", true)),
		read("get_interfaces", "Return the interfaces bound to a zone.",
			[]signature.Param{zoneRequired, permanent}, firewalldListFn("--list-interfaces", true)),
		read("get_rich_rules", "Return a zone's rich rules, in firewalld's own spelling.",
			[]signature.Param{zoneRequired, permanent}, firewalldRichRulesFn),

		mutate("new_zone", "Create a zone in the permanent configuration.",
			[]signature.Param{zoneRequired, restart}, firewalldNewZoneFn),
		mutate("delete_zone", "Delete a zone from the permanent configuration. Refuses the default zone.",
			[]signature.Param{zoneRequired, restart}, firewalldDeleteZoneFn),
		mutate("add_service", "Allow a service in a zone.",
			[]signature.Param{req("service", signature.String, "The service, such as `http`."), zoneOptional, permanent},
			firewalldMemberFn(firewalldServices, "service", true)),
		mutate("remove_service", "Stop allowing a service in a zone.",
			[]signature.Param{req("service", signature.String, "The service, such as `http`."), zoneOptional, permanent},
			firewalldMemberFn(firewalldServices, "service", false)),
		mutate("add_port", "Allow a port in a zone.",
			[]signature.Param{zoneRequired, req("port", signature.String, "The port, as `8080/tcp` or `9000-9001/udp`."), permanent},
			firewalldMemberFn(firewalldPorts, "port", true)),
		mutate("remove_port", "Stop allowing a port in a zone.",
			[]signature.Param{zoneRequired, req("port", signature.String, "The port, as `8080/tcp` or `9000-9001/udp`."), permanent},
			firewalldMemberFn(firewalldPorts, "port", false)),
		mutate("add_source", "Bind a source address or network to a zone.",
			[]signature.Param{zoneRequired, req("source", signature.String, "An address or network, such as `192.0.2.0/24`."), permanent},
			firewalldMemberFn(firewalldSources, "source", true)),
		mutate("remove_source", "Unbind a source address or network from a zone.",
			[]signature.Param{zoneRequired, req("source", signature.String, "An address or network, such as `192.0.2.0/24`."), permanent},
			firewalldMemberFn(firewalldSources, "source", false)),
		mutate("add_rich_rule", "Add a rich rule to a zone. Presence is asked of firewalld, which compares rules by meaning, not spelling.",
			[]signature.Param{zoneRequired, req("rule", signature.String, "The rich rule, in firewalld's rich language."), permanent},
			firewalldMemberFn(firewalldRichRules, "rule", true)),
		mutate("remove_rich_rule", "Remove a rich rule from a zone.",
			[]signature.Param{zoneRequired, req("rule", signature.String, "The rich rule, in firewalld's rich language."), permanent},
			firewalldMemberFn(firewalldRichRules, "rule", false)),
		mutate("reload_rules", "Reload firewalld, so the running firewall matches the permanent configuration. "+
			"Runtime-only changes on every zone are discarded.", nil, firewalldReloadFn),
	}
}

func firewalldStateModule() states.Module {
	return states.Module{
		Sig: signature.Signature{
			Module: "firewalld", Function: "present",
			Doc: "Ensure a zone exists in the permanent configuration with the services, ports, sources and rich " +
				"rules given, and reload firewalld when anything changed.",
			Params: []signature.Param{
				nameParam("The zone. Defaults to the state ID."),
				opt("default", signature.Bool, false,
					"Require this zone to be the default zone. It is checked, not set: see the module's documentation."),
				opt("services", signature.List, nil, "Services the zone must allow."),
				opt("prune_services", signature.Bool, false, "Remove services the zone allows that are not listed."),
				opt("ports", signature.List, nil, "Ports the zone must allow, as `8080/tcp` or `9000-9001/udp`."),
				opt("prune_ports", signature.Bool, false, "Remove ports the zone allows that are not listed."),
				opt("sources", signature.List, nil, "Source addresses or networks bound to the zone."),
				opt("prune_sources", signature.Bool, false, "Remove sources bound to the zone that are not listed."),
				opt("rich_rules", signature.List, nil, "Rich rules the zone must have. Never pruned: see the module's documentation."),
			},
			Mutates: true, TestMode: signature.TestReliable, Privileges: []string{"root"},
			Platforms: linuxOnly, Section: "15.5",
		},
		Fn: firewalldPresentState,
	}
}

// ---- running firewall-cmd ----

func firewalldToolPresent(c *exec.Context) error {
	if c.Which("firewall-cmd") == "" {
		return errors.New("this node has no firewall-cmd; firewalld is not installed")
	}
	return nil
}

// firewalldRun runs firewall-cmd and returns its standard output, or an
// error carrying firewalld's own message. firewall-cmd prints its errors
// as `Error: INVALID_ZONE: ...` on standard error with a code per
// failure (112, 101, 102, ...), and that line says more than any
// wrapping here could.
func firewalldRun(c *exec.Context, args ...string) (string, error) {
	if err := firewalldToolPresent(c); err != nil {
		return "", err
	}
	argv := append([]string{"firewall-cmd"}, args...)
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return "", fmt.Errorf("%s could not be run: %w", strings.Join(argv, " "), err)
	}
	if res.Code != 0 {
		return "", firewalldFailure(argv, res)
	}
	return res.Stdout, nil
}

func firewalldFailure(argv []string, res exec.Result) error {
	msg := strings.TrimSpace(res.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(res.Stdout)
	}
	return fmt.Errorf("%s failed (exit %d): %s", exec.Command{Argv: argv}.String(), res.Code, msg)
}

// firewalldQuery asks one `--query-*` question. firewall-cmd answers
// with exit 0 for yes and exit 1 for no; anything else is a failure to
// answer, such as a zone that does not exist (112).
func firewalldQuery(c *exec.Context, args ...string) (bool, error) {
	if err := firewalldToolPresent(c); err != nil {
		return false, err
	}
	argv := append([]string{"firewall-cmd"}, args...)
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return false, fmt.Errorf("%s could not be run: %w", strings.Join(argv, " "), err)
	}
	switch res.Code {
	case 0:
		return true, nil
	case 1:
		return false, nil
	}
	return false, firewalldFailure(argv, res)
}

// firewalldScope is the part of the argument vector every per-zone
// command shares: `--permanent` first, then the zone when one was given.
// An empty zone leaves the flag off, and firewall-cmd acts on the
// default zone, which is what Salt's `zone=None` means.
func firewalldScope(permanent bool, zone string) []string {
	var out []string
	if permanent {
		out = append(out, "--permanent")
	}
	if zone != "" {
		out = append(out, "--zone="+zone)
	}
	return out
}

// firewalldWords splits a space-separated listing. An empty listing is
// a single newline from firewall-cmd, not nothing, and Fields drops it.
func firewalldWords(out string) []string {
	words := strings.Fields(out)
	if words == nil {
		return []string{}
	}
	return words
}

// firewalldLines splits `--list-rich-rules`, which is one rule per line:
// a rule has spaces in it, so the space-separated reading every other
// listing gets would cut each rule into its words.
func firewalldLines(out string) []string {
	lines := []string{}
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// ---- reading ----

func firewalldVersionFn(c *exec.Context, _ *value.Map) (any, error) {
	out, err := firewalldRun(c, "--version")
	if err != nil {
		return nil, err
	}
	return strings.TrimSpace(out), nil
}

func firewalldDefaultZone(c *exec.Context) (string, error) {
	out, err := firewalldRun(c, "--get-default-zone")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func firewalldDefaultZoneFn(c *exec.Context, _ *value.Map) (any, error) {
	return firewalldDefaultZone(c)
}

// firewalldListFn is every read that is one space-separated listing.
func firewalldListFn(flag string, perZone bool) exec.Func {
	return func(c *exec.Context, args *value.Map) (any, error) {
		zone := ""
		if perZone {
			zone = strings.TrimSpace(states.Str(args, "zone", ""))
		}
		scope := firewalldScope(states.Bool(args, "permanent", true), zone)
		out, err := firewalldRun(c, append(scope, flag)...)
		if err != nil {
			return nil, err
		}
		return stringsToAny(firewalldWords(out)), nil
	}
}

func firewalldRichRulesFn(c *exec.Context, args *value.Map) (any, error) {
	rules, err := firewalldRichRules.list(c, states.Bool(args, "permanent", true),
		strings.TrimSpace(states.Str(args, "zone", "")))
	if err != nil {
		return nil, err
	}
	return stringsToAny(rules), nil
}

// ---- one kind of zone member ----

// firewalldMember is one of the four things a zone holds that this
// module manages. They differ only in firewall-cmd's flag names and in
// how a listing splits, so they share one implementation rather than
// four copies of the same query-then-change dance.
type firewalldMember struct {
	// flag is the stem of firewall-cmd's options: `service` gives
	// --list-services, --query-service, --add-service, --remove-service.
	flag string
	// listFlag is the listing option, which is not always the stem
	// pluralised (`--list-rich-rules`).
	listFlag string
	// perLine marks a listing that is one entry per line rather than
	// space-separated.
	perLine bool
	// echoed says firewalld lists an entry exactly as it was given, so a
	// listing can be compared against a caller's spelling. True for
	// services, ports and sources (captured); false for rich rules, which
	// are reprinted with every value quoted.
	echoed bool
}

var (
	firewalldServices  = firewalldMember{flag: "service", listFlag: "--list-services", echoed: true}
	firewalldPorts     = firewalldMember{flag: "port", listFlag: "--list-ports", echoed: true}
	firewalldSources   = firewalldMember{flag: "source", listFlag: "--list-sources", echoed: true}
	firewalldRichRules = firewalldMember{flag: "rich-rule", listFlag: "--list-rich-rules", perLine: true}
)

func (m firewalldMember) list(c *exec.Context, permanent bool, zone string) ([]string, error) {
	out, err := firewalldRun(c, append(firewalldScope(permanent, zone), m.listFlag)...)
	if err != nil {
		return nil, err
	}
	if m.perLine {
		return firewalldLines(out), nil
	}
	return firewalldWords(out), nil
}

func (m firewalldMember) query(c *exec.Context, permanent bool, zone, item string) (bool, error) {
	return firewalldQuery(c, append(firewalldScope(permanent, zone), "--query-"+m.flag+"="+item)...)
}

func (m firewalldMember) change(c *exec.Context, permanent bool, zone, item string, add bool) error {
	verb := "--remove-"
	if add {
		verb = "--add-"
	}
	_, err := firewalldRun(c, append(firewalldScope(permanent, zone), verb+m.flag+"="+item)...)
	return err
}

// firewalldMemberFn is add_service, remove_port and the rest: ask, then
// change only if the answer says there is something to change.
//
// firewall-cmd would accept the redundant change anyway — it prints
// `Warning: ALREADY_ENABLED` and exits 0 — but then "did anything
// change?" would have to be read out of a warning whose wording differs
// between runtime and permanent (`'http' already in 'zone'` against
// `http`), and test mode could not answer at all.
func firewalldMemberFn(m firewalldMember, param string, add bool) exec.Func {
	return func(c *exec.Context, args *value.Map) (any, error) {
		item := strings.TrimSpace(states.Str(args, param, ""))
		if item == "" {
			return nil, fmt.Errorf("a %s must be given", param)
		}
		zone := strings.TrimSpace(states.Str(args, "zone", ""))
		permanent := states.Bool(args, "permanent", true)
		where := firewalldWhere(permanent, zone)

		present, err := m.query(c, permanent, zone, item)
		if err != nil {
			return nil, err
		}
		if present == add {
			state := "already in"
			if !add {
				state = "not in"
			}
			return iptablesMutateResult(c, false, fmt.Sprintf("The %s `%s` is %s %s.", m.flag, item, state, where), nil), nil
		}
		var change *value.Map
		var done, would string
		if add {
			change = value.MapOf(item, states.Change(nil, "present"))
			done, would = "was added to", "would be added to"
		} else {
			change = value.MapOf(item, states.Change("present", nil))
			done, would = "was removed from", "would be removed from"
		}
		if c.Test {
			return iptablesMutateResult(c, true, fmt.Sprintf("The %s `%s` %s %s.", m.flag, item, would, where), change), nil
		}
		if err := m.change(c, permanent, zone, item, add); err != nil {
			return nil, err
		}
		return iptablesMutateResult(c, true, fmt.Sprintf("The %s `%s` %s %s.", m.flag, item, done, where), change), nil
	}
}

func firewalldWhere(permanent bool, zone string) string {
	name := "the default zone"
	if zone != "" {
		name = "zone " + zone
	}
	if permanent {
		return name + " (permanent)"
	}
	return name + " (runtime)"
}

// ---- zones ----

// firewalldZoneName is the check on a zone this module is asked to
// create.
//
// firewall-cmd's own is not enough. It refuses a name longer than 17
// characters (`INVALID_NAME ... max is 17`, on 0.9.11 and 1.3.4 alike),
// but it accepted `bad/name` on both and wrote it to
// /etc/firewalld/zones/bad/name.xml — a zone whose name is a path, in a
// subdirectory nothing else reads, created during this module's own
// capture. So the name is held to the characters firewalld's shipped
// zones use (`nm-shared`, `public`) and the length it enforces itself.
var firewalldZoneNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,17}$`)

func firewalldZoneName(zone string) error {
	if !firewalldZoneNamePattern.MatchString(zone) {
		return fmt.Errorf("%q is not a zone name this module will create: use 1 to 17 letters, digits, "+
			"`-` or `_` (firewall-cmd itself accepts a `/` and writes the zone into a subdirectory)", zone)
	}
	return nil
}

func firewalldZoneExists(c *exec.Context, zone string) (bool, error) {
	out, err := firewalldRun(c, "--permanent", "--get-zones")
	if err != nil {
		return false, err
	}
	for _, z := range firewalldWords(out) {
		if z == zone {
			return true, nil
		}
	}
	return false, nil
}

func firewalldReload(c *exec.Context) error {
	_, err := firewalldRun(c, "--reload")
	return err
}

func firewalldNewZoneFn(c *exec.Context, args *value.Map) (any, error) {
	zone := strings.TrimSpace(states.Str(args, "zone", ""))
	if err := firewalldZoneName(zone); err != nil {
		return nil, err
	}
	exists, err := firewalldZoneExists(c, zone)
	if err != nil {
		return nil, err
	}
	if exists {
		return iptablesMutateResult(c, false, fmt.Sprintf("The zone %s exists.", zone), nil), nil
	}
	change := value.MapOf(zone, states.Change(nil, "present"))
	if c.Test {
		return iptablesMutateResult(c, true, fmt.Sprintf("The zone %s would be created.", zone), change), nil
	}
	if _, err := firewalldRun(c, "--permanent", "--new-zone="+zone); err != nil {
		return nil, err
	}
	comment := fmt.Sprintf("The zone %s was created.", zone)
	if states.Bool(args, "restart", true) {
		if err := firewalldReload(c); err != nil {
			return nil, fmt.Errorf("the zone %s was created but firewalld did not reload: %w", zone, err)
		}
		comment += " Firewalld was reloaded."
	}
	return iptablesMutateResult(c, true, comment, change), nil
}

// firewalldDeleteZoneFn refuses the default zone. Deleting it would
// leave every interface with no explicit zone — on the hosts this was
// built against, the one carrying SSH — in a zone that no longer
// exists, and nothing firewalld does next in that case has been
// observed here.
func firewalldDeleteZoneFn(c *exec.Context, args *value.Map) (any, error) {
	zone := strings.TrimSpace(states.Str(args, "zone", ""))
	if zone == "" {
		return nil, errors.New("a zone must be given")
	}
	def, err := firewalldDefaultZone(c)
	if err != nil {
		return nil, err
	}
	if zone == def {
		return nil, fmt.Errorf("%s is the default zone, and this module will not delete it", zone)
	}
	exists, err := firewalldZoneExists(c, zone)
	if err != nil {
		return nil, err
	}
	if !exists {
		return iptablesMutateResult(c, false, fmt.Sprintf("The zone %s does not exist.", zone), nil), nil
	}
	change := value.MapOf(zone, states.Change("present", nil))
	if c.Test {
		return iptablesMutateResult(c, true, fmt.Sprintf("The zone %s would be deleted.", zone), change), nil
	}
	if _, err := firewalldRun(c, "--permanent", "--delete-zone="+zone); err != nil {
		return nil, err
	}
	comment := fmt.Sprintf("The zone %s was deleted.", zone)
	if states.Bool(args, "restart", true) {
		if err := firewalldReload(c); err != nil {
			return nil, fmt.Errorf("the zone %s was deleted but firewalld did not reload: %w", zone, err)
		}
		comment += " Firewalld was reloaded."
	}
	return iptablesMutateResult(c, true, comment, change), nil
}

func firewalldReloadFn(c *exec.Context, _ *value.Map) (any, error) {
	if err := firewalldToolPresent(c); err != nil {
		return nil, err
	}
	if c.Test {
		return iptablesMutateResult(c, true, "Firewalld would be reloaded.", nil), nil
	}
	if err := firewalldReload(c); err != nil {
		return nil, err
	}
	return iptablesMutateResult(c, true, "Firewalld was reloaded.", nil), nil
}

// ---- the state ----

// firewalldPresentState is Salt's `firewalld.present`, narrowed to what
// was demonstrated.
//
// What it leaves out is refused rather than ignored, because the
// signature rejects a parameter it does not declare: a migrated tree
// that sets `masquerade`, `interfaces`, `port_fwd`, `block_icmp` or
// `prune_rich_rules` fails loudly instead of converging on less than it
// asked for. `interfaces` in particular moves the interface a node is
// reached over, and it was not driven here for exactly that reason.
//
// `default: true` is checked and never set. Salt sets the default zone
// when it differs; doing so moves every interface without an explicit
// zone, which on the hosts this was built against is the SSH session's,
// so it was the one change this work could not make to find out. A
// state that asks for a zone to be the default and finds another is
// failed, with the command that would make it so.
func firewalldPresentState(c *exec.Context, args *value.Map) (states.Result, error) {
	zone := strings.TrimSpace(states.Str(args, "name", ""))
	if err := firewalldZoneName(zone); err != nil {
		return states.False(err.Error()), nil
	}
	if states.Bool(args, "default", false) {
		def, err := firewalldDefaultZone(c)
		if err != nil {
			return states.False(err.Error()), nil
		}
		if def != zone {
			return states.False(fmt.Sprintf("%s is not the default zone (%s is), and this state does not change "+
				"the default zone: doing so moves every interface without a zone of its own. Run "+
				"`firewall-cmd --set-default-zone=%s` if that is what is meant.", zone, def, zone)), nil
		}
	}

	exists, err := firewalldZoneExists(c, zone)
	if err != nil {
		return states.False(err.Error()), nil
	}
	changes := value.NewMap(0)
	if !exists {
		changes.Set("zone", states.Change(nil, zone))
		if !c.Test {
			if _, err := firewalldRun(c, "--permanent", "--new-zone="+zone); err != nil {
				return states.False(err.Error()), nil
			}
		}
	}

	kinds := []struct {
		member firewalldMember
		key    string
		prune  string
	}{
		{firewalldServices, "services", "prune_services"},
		{firewalldPorts, "ports", "prune_ports"},
		{firewalldSources, "sources", "prune_sources"},
		{firewalldRichRules, "rich_rules", ""},
	}
	for _, k := range kinds {
		wanted := states.Strings(args, k.key)
		prune := k.prune != "" && states.Bool(args, k.prune, false)
		if len(wanted) == 0 && !prune {
			continue
		}
		change, err := firewalldConverge(c, k.member, zone, exists, wanted, prune)
		if err != nil {
			return states.False(err.Error()), nil
		}
		if change != nil {
			changes.Set(k.key, change)
		}
	}

	if changes.Len() == 0 {
		return states.True(fmt.Sprintf("The zone %s is as declared.", zone)), nil
	}
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The zone %s would be changed and firewalld reloaded.", zone), changes), nil
	}
	if err := firewalldReload(c); err != nil {
		return states.False(fmt.Sprintf("The zone %s was changed but firewalld did not reload: %v", zone, err)), nil
	}
	return states.Changed(fmt.Sprintf("The zone %s was changed and firewalld reloaded.", zone), changes), nil
}

// firewalldConverge brings one kind of member of a zone's permanent
// configuration to what was declared, and returns the change, or nil.
//
// A zone that does not exist yet (test mode, before it is created) has
// nothing in it, so everything wanted is an addition and nothing is
// asked of firewall-cmd, which would answer INVALID_ZONE.
func firewalldConverge(c *exec.Context, m firewalldMember, zone string, exists bool,
	wanted []string, prune bool) (*value.Map, error) {
	var current []string
	if exists {
		var err error
		if current, err = m.list(c, true, zone); err != nil {
			return nil, err
		}
	}
	var added, removed []string

	if prune {
		// Only reachable for the echoed kinds, where string equality is
		// firewalld's own identity; see the module comment.
		want := map[string]bool{}
		for _, w := range wanted {
			want[w] = true
		}
		kept := map[string]bool{}
		for _, have := range current {
			if want[have] {
				kept[have] = true
				continue
			}
			removed = append(removed, have)
		}
		for _, w := range wanted {
			if !kept[w] {
				added = append(added, w)
			}
		}
	} else {
		for _, w := range wanted {
			present := false
			if exists {
				var err error
				if present, err = m.query(c, true, zone, w); err != nil {
					return nil, err
				}
			}
			if !present {
				added = append(added, w)
			}
		}
	}
	added = firewalldDedupe(added)
	if len(added) == 0 && len(removed) == 0 {
		return nil, nil
	}
	if !c.Test {
		for _, item := range removed {
			if err := m.change(c, true, zone, item, false); err != nil {
				return nil, err
			}
		}
		for _, item := range added {
			if err := m.change(c, true, zone, item, true); err != nil {
				return nil, err
			}
		}
	}
	out := value.NewMap(2)
	if len(added) > 0 {
		out.Set("added", stringsToAny(added))
	}
	if len(removed) > 0 {
		out.Set("removed", stringsToAny(removed))
	}
	return out, nil
}

func firewalldDedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
