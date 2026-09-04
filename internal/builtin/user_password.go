package builtin

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
)

// Setting a password means handling a hash, and the obvious ways of doing
// it are all wrong. `usermod -p <hash>` and `pw usermod -h <fd>` with the
// hash in the vector both put it in the process table, where any
// unprivileged account on the machine can read it for as long as the
// command runs. Both tools can take it on standard input instead, and
// that is the only way this module writes one.
//
// The hash is never logged, never returned in `changes`, and never
// appears in a comment. What the state reports is that the password
// changed, which is all an operator needs and all a job return should
// carry.

// passwordFile is where the platform keeps the hashes, and the field they
// live in. Both files are readable only by root, which is the same
// privilege setting a password needs, so a state that cannot read the
// current hash cannot have applied one either.
type passwordFile struct {
	path  string
	field int
}

var hashLocations = map[string]passwordFile{
	// FreeBSD: name:hash:uid:gid:class:change:expire:gecos:home:shell
	"freebsd": {"/etc/master.passwd", 1}, // lexicon:allow — the filename FreeBSD uses
	// Linux: name:hash:lastchange:min:max:warn:inactive:expire
	"linux": {"/etc/shadow", 1},
}

// currentHash reads an account's stored password hash.
//
// A missing account is not an error here: the caller is deciding whether
// to set a password on an account it may be about to create.
func currentHash(name string) (hash string, found bool, err error) {
	if runtime.GOOS == "darwin" {
		return currentHashDarwin(name)
	}
	if runtime.GOOS == "windows" {
		return currentHashWindows(name)
	}
	loc, ok := hashLocations[runtime.GOOS]
	if !ok {
		return "", false, fmt.Errorf("this build does not know where %s keeps password hashes", runtime.GOOS)
	}
	f, err := os.Open(loc.path)
	if err != nil {
		if os.IsPermission(err) {
			return "", false, fmt.Errorf("reading %s needs root, and so does setting a password", loc.path)
		}
		return "", false, err
	}
	defer f.Close()

	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := scan.Text()
		if !strings.HasPrefix(line, name+":") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) <= loc.field {
			return "", false, fmt.Errorf("the entry for %s in %s is malformed", name, loc.path)
		}
		return fields[loc.field], true, nil
	}
	if err := scan.Err(); err != nil {
		return "", false, err
	}
	return "", false, nil
}

// currentHashDarwin is the macOS half of currentHash.
//
// macOS keeps no crypt-style shadow file to read a line out of: a
// password hash lives in Open Directory, in the per-user ShadowHashData
// attribute, as a SALTED-SHA512-PBKDF2 dictionary inside a binary plist.
// Reading it — and the plaintext-based dscl/sysadminctl calls that write
// one — is real, separate work for the mac_shadow module of SPEC section
// 15.3, not yet built.
//
// dscl will not tell this apart from a missing attribute: querying
// ShadowHashData unprivileged answers "No such key" with exit 0 either
// way, the same as it would for an account that genuinely has none. So
// the permission check has to happen before asking, by euid, rather than
// from dscl's answer — confirmed against a real `dscl -plist . -read`
// call on this host.
func currentHashDarwin(name string) (string, bool, error) {
	if os.Geteuid() != 0 {
		return "", false, fmt.Errorf("reading a macOS account's password hash needs root, and so does setting one")
	}
	return "", false, fmt.Errorf("this build does not yet read a macOS account's password hash (mac_shadow, SPEC 15.3)")
}

// currentHashWindows says why there is no hash to read.
//
// Windows keeps password verifiers in the SAM and exposes no documented
// way to read one, at any privilege. That is not a gap in this build:
// there is nothing there to read. It matters because the caller uses
// the current hash to decide whether a password needs setting, so on
// this platform that question cannot be answered and a password state
// cannot be idempotent — which is what the caller has to be told,
// rather than "this build does not know where windows keeps password
// hashes", which reads as a missing table entry.
func currentHashWindows(name string) (string, bool, error) {
	return "", false, fmt.Errorf(
		"Windows keeps password verifiers in the SAM and exposes no way to read one, " +
			"so a password cannot be compared before it is set; SPEC 15.3 puts setting " +
			"one in win_shadow, which this build does not have")
}

// passwordCommand builds the command that writes a hash, with the hash on
// standard input. It is separate from setPassword so that the property
// that matters — the hash is never in the argument vector — can be
// asserted without either tool being installed.
func passwordCommand(platform, name, hash string) (exec.Command, error) {
	switch platform {
	case "freebsd":
		// `-H 0` tells pw to read an already-hashed password from the
		// given file descriptor. 0 is standard input.
		return exec.Command{
			Argv:  []string{"pw", "usermod", "-n", name, "-H", "0"},
			Stdin: hash + "\n",
		}, nil
	case "linux":
		// chpasswd -e reads `name:hash` lines. usermod -p would put the
		// hash in the process table.
		return exec.Command{
			Argv:  []string{"chpasswd", "-e"},
			Stdin: name + ":" + hash + "\n",
		}, nil
	}
	return exec.Command{}, fmt.Errorf("this build cannot set a password on %s", platform)
}

// setPassword writes a hash for an account, passing it on standard input.
func setPassword(c *exec.Context, tool accountTool, name, hash string) error {
	if tool.Platform == "linux" && c.Which("chpasswd") == "" {
		return fmt.Errorf("setting a password needs chpasswd, which was not found on this node")
	}
	cmd, err := passwordCommand(tool.Platform, name, hash)
	if err != nil {
		return err
	}
	res, err := c.Run(cmd)
	if err != nil {
		return err
	}
	if res.Code != 0 {
		// The output of these tools does not carry the hash, but it is
		// not worth assuming so: only the exit status is reported.
		return fmt.Errorf("%s exited %d", cmd.Argv[0], res.Code)
	}
	return nil
}
