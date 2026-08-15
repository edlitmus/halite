package modules

import (
	"strings"
	"testing"
)

func TestZfsSizesAreComparedAsSizes(t *testing.T) {
	// A state asking for `quota: 10G` must not fight zfs over how it spells
	// the same number back, or the property is set on every run forever.
	equal := []struct{ want, have string }{
		{"10G", "10G"},
		{"10G", "10.0G"},
		{"1G", "1024M"},
		{"10Gi", "10G"},
		{"none", "0"},
		{"none", "none"},
		{"lz4", "lz4"},
		{"off", "OFF"},
	}
	for _, tc := range equal {
		if !zfsValuesEqual(tc.want, tc.have) {
			t.Errorf("%q and %q are the same value", tc.want, tc.have)
		}
	}
	different := []struct{ want, have string }{
		{"10G", "20G"},
		{"10G", "10M"},
		{"lz4", "zstd"},
		{"on", "off"},
		{"10G", "none"},
	}
	for _, tc := range different {
		if zfsValuesEqual(tc.want, tc.have) {
			t.Errorf("%q and %q are not the same value", tc.want, tc.have)
		}
	}
}

func TestZfsSizeParsing(t *testing.T) {
	cases := []struct {
		in    string
		want  int64
		valid bool
	}{
		{"1024", 1024, true},
		{"1K", 1 << 10, true},
		{"1M", 1 << 20, true},
		{"1G", 1 << 30, true},
		{"1T", 1 << 40, true},
		{"1.5G", 1<<30 + 1<<29, true},
		{"none", 0, true},
		{"-", 0, true},
		{"lz4", 0, false},
		{"10X", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := zfsSize(tc.in)
			if ok != tc.valid {
				t.Fatalf("want valid=%v, got %v", tc.valid, ok)
			}
			if ok && got != tc.want {
				t.Fatalf("want %d, got %d", tc.want, got)
			}
		})
	}
}

func TestZfsPropertiesAreSortedAndChecked(t *testing.T) {
	pairs, err := zfsProperties(map[string]any{
		"properties": map[string]any{"quota": "10G", "compression": "lz4", "atime": "off"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"atime=off", "compression=lz4", "quota=10G"}
	if strings.Join(pairs, " ") != strings.Join(want, " ") {
		t.Fatalf("want %v sorted, got %v", want, pairs)
	}

	if _, err := zfsProperties(map[string]any{"properties": []any{"compression=lz4"}}); err == nil {
		t.Fatal("a list of properties should be reported, not ignored")
	}
	if _, err := zfsProperties(map[string]any{
		"properties": map[string]any{"not a name": "x"},
	}); err == nil {
		t.Fatal("a property name with a space should be reported")
	}
	if pairs, err := zfsProperties(map[string]any{}); err != nil || pairs != nil {
		t.Fatal("no properties is not an error")
	}
}

func TestSnapshotAndDatasetNamesAreNotMixedUp(t *testing.T) {
	if err := zfsAvailable(); err != nil {
		t.Skip("no zfs")
	}
	if r := zfsFilesystemPresent(&Ctx{}, "pool/data@snap", nil); r.Ok {
		t.Fatal("a snapshot name should not be created as a filesystem")
	}
	for _, fn := range []func(*Ctx, string, map[string]any) Result{
		zfsSnapshotPresent, zfsSnapshotAbsent,
	} {
		if r := fn(&Ctx{}, "pool/data", nil); r.Ok {
			t.Fatal("a dataset name is not a snapshot name")
		}
	}
}

// TestZfsAcceptsWhatHaliteWouldCreate checks the create against zfs's own
// dry run, which validates the dataset name and every property without
// creating anything. It is the same idea as parsing a jail.conf with
// jail(8): my idea of the property set is not the property set.
func TestZfsAcceptsWhatHaliteWouldCreate(t *testing.T) {
	if err := zfsAvailable(); err != nil {
		t.Skip("no zfs")
	}
	pool, ok := firstZfsPool()
	if !ok {
		t.Skip("no zfs pool to validate against")
	}
	properties, err := zfsProperties(map[string]any{
		"properties": map[string]any{
			"compression": "lz4",
			"atime":       "off",
			"quota":       "10G",
			"mountpoint":  "/usr/local/jails/halite-dry-run",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	argv := []string{"create", "-n"}
	for _, pair := range properties {
		argv = append(argv, "-o", pair)
	}
	// -n stops before anything is created, so this names a dataset that
	// does not exist and leaves it that way.
	argv = append(argv, pool+"/halite-dry-run-check")

	out, errOut, rc, err := run("zfs", argv...)
	if err != nil || rc != 0 {
		t.Fatalf("zfs rejected what halite would create (rc=%d, %v):\nzfs %s\n%s%s",
			rc, err, strings.Join(argv, " "), out, errOut)
	}
	if zfsExists(pool + "/halite-dry-run-check") {
		t.Fatal("the dry run created a dataset")
	}
}

// firstZfsPool returns a pool to validate names against, without needing
// one to exist by a particular name.
func firstZfsPool() (string, bool) {
	out, ok := pkgQuery("zpool", "list", "-H", "-o", "name")
	if !ok {
		return "", false
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line, true
		}
	}
	return "", false
}
