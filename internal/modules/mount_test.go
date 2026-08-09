package modules

import "testing"

func TestParseFstabFormatReadsMountLines(t *testing.T) {
	text := `# device	mountpoint	fstype	options	dump	pass
/dev/ada0p2	/	ufs	rw	1	1
/dev/ada1p1	/data	ufs	rw,noatime	2	2

tmpfs	/tmp	tmpfs	rw,mode=1777
`
	entries := parseFstabFormat(text)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3: %v", len(entries), entries)
	}

	data := entries["/data"]
	want := mountEntry{Device: "/dev/ada1p1", Point: "/data", FSType: "ufs", Opts: "rw,noatime", Dump: "2", Pass: "2"}
	if data != want {
		t.Errorf("/data = %+v, want %+v", data, want)
	}
	// Linux-style lines carry no dump/pass columns; those default rather
	// than shifting the other fields.
	if tmp := entries["/tmp"]; tmp.Dump != "0" || tmp.Pass != "0" || tmp.Opts != "rw,mode=1777" {
		t.Errorf("/tmp = %+v", tmp)
	}
}

func TestParseFstabFormatSkipsCommentsAndJunk(t *testing.T) {
	entries := parseFstabFormat("# just a comment\n\n   \nbroken line\n")
	if len(entries) != 0 {
		t.Errorf("got %v, want nothing parsed", entries)
	}
}

func TestParseDarwinMounts(t *testing.T) {
	text := `/dev/disk3s1s1 on / (apfs, sealed, local, read-only, journaled)
devfs on /dev (devfs, local, nobrowse)
`
	entries := parseDarwinMounts(text)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %v", len(entries), entries)
	}
	root := entries["/"]
	if root.Device != "/dev/disk3s1s1" || root.FSType != "apfs" {
		t.Errorf("/ = %+v", root)
	}
}

func TestFstabLineIsTabSeparatedInFieldOrder(t *testing.T) {
	entry := mountEntry{
		Device: "/dev/ada1p1", Point: "/data", FSType: "ufs",
		Opts: "rw,noatime", Dump: "2", Pass: "2",
	}
	want := "/dev/ada1p1\t/data\tufs\trw,noatime\t2\t2"
	if got := entry.fstabLine(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Round-tripping a line must produce the same entry, which is what
	// makes drift detection reliable.
	parsed, ok := parseFstabLine(entry.fstabLine())
	if !ok || parsed != entry {
		t.Errorf("round trip gave %+v, ok=%v", parsed, ok)
	}
}
