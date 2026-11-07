//go:build windows

package winsec

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// The entry types a discretionary list can hold, from winnt.h. x/sys
// names only the first two, and this package read every entry as the
// first: GetAce hands back a pointer to the header, and casting that to
// ACCESS_ALLOWED_ACE is right for four of the eight types and wrong for
// the other four (issue 608 item 3, DIVERGENCE 5.618).
const (
	aceAllowed               = 0x0 // header, mask, SID
	aceDenied                = 0x1 // header, mask, SID
	aceAllowedObject         = 0x5 // header, mask, flags, [GUID], [GUID], SID
	aceDeniedObject          = 0x6
	aceAllowedCallback       = 0x9 // header, mask, SID, condition
	aceDeniedCallback        = 0xA
	aceAllowedCallbackObject = 0xB // header, mask, flags, [GUID], [GUID], SID, condition
	aceDeniedCallbackObject  = 0xC
)

// aceEntry is one entry of a list, read at the layout its type has.
type aceEntry struct {
	// typ is the header's AceType, as stored.
	typ uint8
	// flags is the header's AceFlags: inheritance and the inherited mark.
	flags uint8
	mask  uint32
	// sid points into the descriptor the entry came from, and is only
	// valid while that descriptor is reachable. Copy it to keep it.
	sid *windows.SID
	// allow and deny say which way the entry decides, for the types
	// that decide at all. Both are false for any other type.
	allow, deny bool
	// conditional marks a callback entry, whose grant or denial holds
	// only while its condition does. object marks an object entry.
	conditional, object bool
}

// readACE reads the entry GetAce returned, at the layout its type has.
//
// The object types carry a flags word and up to two GUIDs between the
// mask and the SID, and which GUIDs are present is in the flags. Reading
// the SID at offset eight, as a plain entry has it, reads the flags word
// and the first GUID as though they were an account.
func readACE(ace *windows.ACCESS_ALLOWED_ACE) aceEntry {
	e := aceEntry{
		typ:   ace.Header.AceType,
		flags: ace.Header.AceFlags,
		mask:  uint32(ace.Mask),
	}
	base := unsafe.Pointer(ace)
	sidOffset := uintptr(8)
	switch e.typ {
	case aceAllowed, aceDenied, aceAllowedCallback, aceDeniedCallback:
	case aceAllowedObject, aceDeniedObject, aceAllowedCallbackObject, aceDeniedCallbackObject:
		e.object = true
		present := *(*uint32)(unsafe.Add(base, 8))
		sidOffset = 12
		if present&windows.ACE_OBJECT_TYPE_PRESENT != 0 {
			sidOffset += 16
		}
		if present&windows.ACE_INHERITED_OBJECT_TYPE_PRESENT != 0 {
			sidOffset += 16
		}
	default:
		// Not a type a discretionary list may hold. The SID is read at
		// the plain offset so that a listing still names something,
		// and neither allow nor deny is set, so no check counts it.
		e.sid = (*windows.SID)(unsafe.Add(base, sidOffset))
		return e
	}
	e.sid = (*windows.SID)(unsafe.Add(base, sidOffset))
	switch e.typ {
	case aceAllowed, aceAllowedObject, aceAllowedCallback, aceAllowedCallbackObject:
		e.allow = true
	default:
		e.deny = true
	}
	e.conditional = e.typ == aceAllowedCallback || e.typ == aceDeniedCallback ||
		e.typ == aceAllowedCallbackObject || e.typ == aceDeniedCallbackObject
	return e
}
