// Package permtest opens and closes a file's permissions the way the
// running platform does, for tests about who can read what.
//
// It exists because os.Chmod is not that operation on Windows. Several
// tests used a chmod to make a file world-readable, asserted a refusal,
// and got one — for the wrong reason, since os.Stat reports 0666 for
// every writable file there and the check under test never saw a real
// permission at all. A test that cannot make the condition it is testing
// for is not testing for it.
//
// The same thing arrives from the other direction as root, which holds
// CAP_DAC_OVERRIDE: a chmod that denies access succeeds, changes the
// mode, and denies nothing to the account that set it. DenyWrite and
// DenyRead skip rather than pretend, naming the reason, because a
// container is a reasonable place to run this suite and root is its
// default account.
//
// Nothing outside a test imports this. Granting a file to Everyone is
// not an operation a configuration management system should offer.
package permtest
