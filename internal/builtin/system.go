package builtin

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerSystem installs the modules that report on the running system:
// network, dnsutil, status, disk, and hostname.
//
// Everything here reads through the standard library or through a base
// system tool, never through an installed package: a node that has just
// been provisioned and has nothing on it yet still has to be able to
// answer these.
func registerSystem(r *Registries) {
	registerNetworkModule(r)
	registerStatusModule(r)
	registerDiskModule(r)
	registerHostnameModule(r)
}

func registerNetworkModule(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "network", Function: "interfaces",
				Doc:      "Return this node's interfaces and their addresses.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				v, _ := c.Grains.Get("ip_interfaces")
				return v, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "network", Function: "ip_addrs",
				Doc: "Return this node's IPv4 addresses, optionally within a network.",
				Params: []signature.Param{
					opt("cidr", signature.String, "", "Restrict to addresses inside this network."),
					opt("include_loopback", signature.Bool, false, "Include loopback addresses."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return nodeAddresses(c, "ipv4", args)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "network", Function: "ip_addrs6",
				Doc: "Return this node's IPv6 addresses, optionally within a network.",
				Params: []signature.Param{
					opt("cidr", signature.String, "", "Restrict to addresses inside this network."),
					opt("include_loopback", signature.Bool, false, "Include loopback addresses."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return nodeAddresses(c, "ipv6", args)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "network", Function: "connect",
				Doc: "Report whether a TCP connection to a host and port succeeds.",
				Params: []signature.Param{
					req("host", signature.String, "The host."),
					req("port", signature.Int, "The port."),
					opt("timeout", signature.Duration, int64(5), "How long to wait."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				host := states.Str(args, "host", "")
				port := states.Int(args, "port", 0)
				timeout := 5 * time.Second
				if v, ok := args.Get("timeout"); ok && v != nil {
					if d, err := durationOf(v); err == nil {
						timeout = d
					}
				}
				addr := net.JoinHostPort(host, strconv.FormatInt(port, 10))
				conn, err := net.DialTimeout("tcp", addr, timeout)
				if err != nil {
					return value.MapOf("result", false, "comment", err.Error()), nil
				}
				conn.Close()
				return value.MapOf("result", true, "comment", "connected to "+addr), nil
			},
		},
		// `A` and `AAAA` carry Salt's capitals, because they are DNS
		// record types and Salt's functions are named for them. This
		// shipped as `dnsutil.a`, which no tree written for Salt can
		// reach: `salt['dnsutil.A'](id)` is what an estate writes, and it
		// resolved to nothing.
		//
		// They are one family each, as the record types are. The single
		// function this replaces returned whatever the resolver had, so a
		// tree asking for an A record could be handed an IPv6 address and
		// use it as an IPv4 one -- which is the shape of defect that
		// makes the wrong thing happen rather than an error.
		exec.Module{
			Sig: signature.Signature{
				Module: "dnsutil", Function: "A",
				Doc:      "Resolve a name to its IPv4 addresses, as an A record lookup does.",
				Params:   []signature.Param{req("host", signature.String, "The name.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return lookupAddressFamily(states.Str(args, "host", ""), true), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "dnsutil", Function: "AAAA",
				Doc:      "Resolve a name to its IPv6 addresses, as an AAAA record lookup does.",
				Params:   []signature.Param{req("host", signature.String, "The name.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return lookupAddressFamily(states.Str(args, "host", ""), false), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "dnsutil", Function: "hosts_file",
				Doc:      "Return the hosts file as a mapping of address to names.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return c.Call("hosts.list_hosts", value.NewMap(0))
			},
		},
	)
}

// lookupAddressFamily resolves a name to one address family.
//
// An empty list on failure rather than an error, which is what this
// module already did. Salt returns the *string* "Unable to resolve
// <host>" from a function it documents as always returning a list, and
// reproducing that would hand a tree a string where it indexes a list.
func lookupAddressFamily(host string, wantIPv4 bool) []any {
	if host == "" {
		return []any{}
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return []any{}
	}
	out := []any{}
	for _, ip := range ips {
		if (ip.To4() != nil) == wantIPv4 {
			out = append(out, ip.String())
		}
	}
	return out
}

// nodeAddresses filters the address grains, which is cheaper and more
// consistent than asking the kernel a second time.
func nodeAddresses(c *exec.Context, grain string, args *value.Map) (any, error) {
	v, ok := c.Grains.Get(grain)
	if !ok {
		return []any{}, nil
	}
	list, ok := v.([]any)
	if !ok {
		return []any{}, nil
	}

	var network *net.IPNet
	if cidr := states.Str(args, "cidr", ""); cidr != "" {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("%q is not a network: %w", cidr, err)
		}
		network = n
	}
	includeLoopback := states.Bool(args, "include_loopback", false)

	out := []any{}
	for _, item := range list {
		s := value.KeyString(item)
		ip := net.ParseIP(s)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() && !includeLoopback {
			continue
		}
		if network != nil && !network.Contains(ip) {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func registerStatusModule(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "status", Function: "uptime",
				Doc:      "Return this node's uptime in seconds, and as text.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				seconds, err := uptimeSeconds(c)
				if err != nil {
					return nil, err
				}
				d := time.Duration(seconds) * time.Second
				return value.MapOf(
					"seconds", seconds,
					"since", time.Now().Add(-d).UTC().Format(time.RFC3339),
					"human", d.Round(time.Minute).String(),
				), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "status", Function: "loadavg",
				Doc:      "Return the one, five, and fifteen minute load averages.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return loadAverages(c)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "status", Function: "meminfo",
				Doc:      "Return this node's memory totals, in mebibytes.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				total, _ := c.Grains.Get("mem_total")
				swap, _ := c.Grains.Get("swap_total")
				return value.MapOf("mem_total", total, "swap_total", swap), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "status", Function: "time",
				Doc: "Return this node's clock.",
				Params: []signature.Param{
					opt("format", signature.String, time.RFC3339, "A Go time layout."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return time.Now().Format(states.Str(args, "format", time.RFC3339)), nil
			},
		},
	)
}

// uptimeSeconds reads the uptime from whichever source the platform has.
func uptimeSeconds(c *exec.Context) (int64, error) {
	// Linux: /proc/uptime, which needs no tool at all.
	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) > 0 {
			if f, err := strconv.ParseFloat(fields[0], 64); err == nil {
				return int64(f), nil
			}
		}
	}
	// The BSDs and macOS: kern.boottime, which sysctl prints as a struct.
	res, err := c.Run(exec.Command{Argv: []string{"sysctl", "-n", "kern.boottime"}, IgnoreExitCode: true})
	if err == nil && res.Code == 0 {
		if sec := extractBootSeconds(res.Stdout); sec > 0 {
			return time.Now().Unix() - sec, nil
		}
	}
	return 0, fmt.Errorf("this node's uptime could not be read")
}

// extractBootSeconds pulls the epoch out of `{ sec = 1755..., usec = ... }`.
func extractBootSeconds(s string) int64 {
	i := strings.Index(s, "sec = ")
	if i < 0 {
		return 0
	}
	rest := s[i+len("sec = "):]
	end := strings.IndexAny(rest, ", }")
	if end < 0 {
		end = len(rest)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(rest[:end]), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func loadAverages(c *exec.Context) (*value.Map, error) {
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) >= 3 {
			return loadMap(fields[0], fields[1], fields[2]), nil
		}
	}
	res, err := c.Run(exec.Command{Argv: []string{"sysctl", "-n", "vm.loadavg"}, IgnoreExitCode: true})
	if err == nil && res.Code == 0 {
		fields := strings.Fields(strings.Trim(strings.TrimSpace(res.Stdout), "{}"))
		if len(fields) >= 3 {
			return loadMap(fields[0], fields[1], fields[2]), nil
		}
	}
	return nil, fmt.Errorf("the load average could not be read")
}

func loadMap(one, five, fifteen string) *value.Map {
	parse := func(s string) float64 {
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
	return value.MapOf("1-min", parse(one), "5-min", parse(five), "15-min", parse(fifteen))
}

func registerDiskModule(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "disk", Function: "usage",
				Doc:      "Return filesystem usage, as df reports it.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return diskUsage(c)
			},
		},
	)
}

// diskUsage parses df's portable output, which every unix has and which is
// stable across them in `-P` mode.
func diskUsage(c *exec.Context) (*value.Map, error) {
	res, err := c.Run(exec.Command{Argv: []string{"df", "-Pk"}, IgnoreExitCode: true})
	if err != nil {
		return nil, err
	}
	out := value.NewMap(16)
	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	for i, line := range lines {
		if i == 0 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		out.Set(fields[5], value.MapOf(
			"filesystem", fields[0],
			"1K-blocks", parseInt64(fields[1]),
			"used", parseInt64(fields[2]),
			"available", parseInt64(fields[3]),
			"capacity", fields[4],
		))
	}
	return out, nil
}

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func registerHostnameModule(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "network", Function: "get_hostname",
				Doc:      "Return this node's hostname.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return os.Hostname()
			},
		},
	)
}
