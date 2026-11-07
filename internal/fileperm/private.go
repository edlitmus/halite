package fileperm

import "os"

// Private reports whether a mode asks for the private access list on
// Windows. On unix the mode is applied as it is and this is not asked.
//
// It does when the mode grants other accounts nothing. The group bits
// are not looked at, because a Windows file has no group: 0640 makes two
// statements this platform can carry out, that the owner may read and
// write and that other accounts may not, and one it cannot, that a group
// may read. The two are carried out.
//
// Any group bit used to keep the parent's list instead, so 0640 on a
// return log or a temporary script left it readable by every account the
// parent folder lets in, which is wider than the mode asked for in the
// one respect that can be enforced. The owner chose the narrower reading
// on 2026-11-07 (issue 608 item 4, DIVERGENCE 5.618).
//
// The audit in nocreates_test.go asks the same question of a literal
// mode passed to the standard library, so that the two are one rule.
func Private(mode os.FileMode) bool { return mode.Perm()&0o007 == 0 }
