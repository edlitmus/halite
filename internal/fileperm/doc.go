// Package fileperm says who may read a file, in the terms the platform
// uses.
//
// A POSIX mode is the whole answer on unix and no answer at all on
// Windows, where os.Stat synthesises one from the read-only attribute:
// 0666 for anything writable, 0444 for anything not. Every check of the
// form `mode&0o077 != 0` therefore answered "the world can read it" and
// every chmod to 0600 did nothing.
//
// That is not cosmetic. halite writes a shared signing secret, an
// enrollment key, a job cache, a return log carrying whatever a job
// returned, and a temporary script carrying whatever a state passed to
// it. On unix each is kept from other accounts by its mode. On Windows
// each was kept from nobody, and the one check that would have said so —
// the refusal in ReadSecretFile — instead refused every secret file on
// the platform and told the operator to run a chmod that changes
// nothing.
//
// Apply carries out what a mode asks for. Others answers the question
// the mode was being read for, and names the accounts rather than a
// number, because on one of these platforms the number means nothing.
package fileperm
