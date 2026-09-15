package builtin

// ownerLabel names an ownership setting the way a state's comment says
// it: "root:wheel", "root", or ":wheel".
//
// It lives here rather than beside one platform's chown because the
// states that report ownership are built for every platform, and a
// label is only string formatting. Keeping it under a unix build tag
// compiled fine on this machine and broke the Windows build, which is
// what `GOOS=windows go test -c` is for.
func ownerLabel(u, g string) string {
	switch {
	case u != "" && g != "":
		return u + ":" + g
	case u != "":
		return u
	default:
		return ":" + g
	}
}
