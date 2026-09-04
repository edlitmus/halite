// Package winreg is the Windows registry, for the win_registry module
// of SPEC section 15.3.
//
// It goes through the registry API rather than running reg.exe. There is
// no argument for the binary here that there was for a package manager:
// reg.exe writes a table for a person to read, its type names and its
// error text are localised, and a value containing a newline or a tab
// cannot be told from two values in what it prints. The API returns a
// type and a value.
//
// Two things a caller has to be able to say, because a real estate
// depends on both and getting either wrong writes to the wrong place:
// which hive, and which of the two views a 64-bit Windows keeps. See
// Hive and View.
package winreg
