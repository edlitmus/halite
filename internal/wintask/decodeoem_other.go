//go:build !windows

package wintask

// decodeSingleByte is a Windows-only correction for schtasks' OEM code
// page output; nothing here calls into a real scheduler on another
// platform, so there is nothing to decode.
func decodeSingleByte(raw []byte) string { return string(raw) }
