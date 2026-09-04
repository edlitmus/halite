// Package winsec is the Windows half of "this file is private".
//
// A POSIX mode does not exist on Windows. os.Stat synthesises one from
// the read-only attribute — 0666, or 0444 — so every check of the form
// `mode&0o077 != 0` is answered "yes, the world can read it" and every
// chmod to 0600 does nothing. That is not a cosmetic difference: halite
// writes a shared signing secret, a private key, a return log and a
// temporary script holding whatever a state passed to it, and each is
// kept from other accounts by a mode on unix and by nothing at all here.
//
// The equivalent control is the discretionary access control list. A
// file that grants access only to its owner, to SYSTEM and to the local
// Administrators group, with inheritance from the parent blocked, is as
// private as mode 0600 — and, unlike a mode, it says so in the terms an
// administrator on this platform reads.
package winsec
