package builtin

import (
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The fixtures are real `--reportformat json` output, captured from LVM2
// 2.03 on Ubuntu 24.04 against a throwaway group on loopback devices and
// then trimmed. Every value is a quoted string even where it is a
// number, which is the property the parser is written around: `map[
// string]string` rows, and the numeric columns parsed where they are
// read.

const lvmPVsJSON = `{
      "report": [
          {
              "pv": [
                  {"pv_name":"/dev/loop0", "vg_name":"vgtest", "pv_size":"66060288", "pv_free":"37748736", "pv_uuid":"aaaa11-2222-3333-4444-5555-6666-777788", "pv_attr":"a--"},
                  {"pv_name":"/dev/loop1", "vg_name":"", "pv_size":"66060288", "pv_free":"66060288", "pv_uuid":"bbbb11-2222-3333-4444-5555-6666-777788", "pv_attr":"---"}
              ]
          }
      ]
  }
`

const lvmVGsJSON = `{
      "report": [
          {
              "vg": [
                  {"vg_name":"vgtest", "vg_uuid":"cccc11-2222-3333-4444-5555-6666-777788", "vg_size":"62914560", "vg_free":"37748736", "vg_extent_size":"4194304", "vg_extent_count":"15", "vg_free_count":"9", "pv_count":"1", "lv_count":"1"}
              ]
          }
      ]
  }
`

const lvmLVsJSON = `{
      "report": [
          {
              "lv": [
                  {"lv_name":"data", "vg_name":"vgtest", "lv_uuid":"dddd11-2222-3333-4444-5555-6666-777788", "lv_path":"/dev/vgtest/data", "lv_size":"25165824", "lv_attr":"-wi-a-----", "origin":"", "pool_lv":"", "data_percent":""}
              ]
          }
      ]
  }
`

// The JSON report reads into rows with the byte columns as integers.
func TestLVMParsesAJSONReportIntoRows(t *testing.T) {
	pvs, err := lvmParseReport(lvmPVsJSON, "pv")
	if err != nil {
		t.Fatalf("pv report: %v", err)
	}
	if len(pvs) != 2 {
		t.Fatalf("read %d physical volumes, want 2", len(pvs))
	}
	if pvs[0]["pv_name"] != "/dev/loop0" || pvs[0]["vg_name"] != "vgtest" {
		t.Errorf("first PV read as %+v", pvs[0])
	}
	// A PV that is not in a group carries an empty vg_name, which is what
	// lvmFindPV and the vgextend guard turn on.
	if pvs[1]["vg_name"] != "" {
		t.Errorf("the free PV reads as being in group %q", pvs[1]["vg_name"])
	}
	if b := lvmBytes(pvs[0]["pv_size"]); b != 66060288 {
		t.Errorf("pv_size read as %d, want 66060288", b)
	}

	vgs, err := lvmParseReport(lvmVGsJSON, "vg")
	if err != nil {
		t.Fatalf("vg report: %v", err)
	}
	if len(vgs) != 1 || vgs[0]["vg_name"] != "vgtest" {
		t.Fatalf("vg report read as %+v", vgs)
	}
	if b := lvmBytes(vgs[0]["vg_extent_size"]); b != 4194304 {
		t.Errorf("vg_extent_size read as %d, want 4194304", b)
	}

	lvs, err := lvmParseReport(lvmLVsJSON, "lv")
	if err != nil {
		t.Fatalf("lv report: %v", err)
	}
	if len(lvs) != 1 || lvs[0]["lv_name"] != "data" || lvs[0]["vg_name"] != "vgtest" {
		t.Fatalf("lv report read as %+v", lvs)
	}
	if lvs[0]["lv_attr"] != "-wi-a-----" {
		t.Errorf("lv_attr read as %q", lvs[0]["lv_attr"])
	}
	if b := lvmBytes(lvs[0]["lv_size"]); b != 25165824 {
		t.Errorf("lv_size read as %d, want 25165824", b)
	}
}

// A row rendered for a caller has its size and count columns as integers
// and its names and uuids as strings.
func TestLVMRowValuePresentsBytesAsIntegers(t *testing.T) {
	rows, _ := lvmParseReport(lvmVGsJSON, "vg")
	m := lvmRowValue("vg", rows[0])
	if v, _ := m.GetString("vg_size"); v != int64(62914560) {
		t.Errorf("vg_size rendered as %#v, want int64 62914560", v)
	}
	if v, _ := m.GetString("pv_count"); v != int64(1) {
		t.Errorf("pv_count rendered as %#v, want int64 1", v)
	}
	if v, _ := m.GetString("vg_name"); v != "vgtest" {
		t.Errorf("vg_name rendered as %#v", v)
	}
}

// Anything that is not a JSON report is refused rather than read as an
// empty machine.
func TestLVMRefusesOutputThatIsNotAReport(t *testing.T) {
	for _, bad := range []string{
		"",
		"  WARNING: Running as a non-root user. Functionality may be unavailable.\n",
		"pvs: unrecognized option '--reportformat'\n",
	} {
		if _, err := lvmParseReport(bad, "pv"); err == nil {
			t.Errorf("%q was parsed as a report", bad)
		}
	}
}

// The read command asks for JSON and exact bytes, and never for a
// humanised size.
//
// `--units b --nosuffix` is the load-bearing pair: without it a size
// comes back as `1.50g`, which parses as 1.5 and is wrong by a factor of
// a billion — the same trap `quota` refuses `repquota -h` for.
func TestLVMReportArgvAsksForJSONAndExactBytes(t *testing.T) {
	for _, kind := range []string{"pv", "vg", "lv"} {
		argv := lvmReportArgv(kind, "")
		joined := strings.Join(argv, " ")
		for _, want := range []string{"--reportformat json", "--units b", "--nosuffix"} {
			if !strings.Contains(joined, want) {
				t.Errorf("%s report argv %q does not ask for %q", kind, joined, want)
			}
		}
		for _, forbidden := range []string{"-h", "--human-readable", "--units h", "--units H"} {
			for _, a := range argv {
				if a == forbidden {
					t.Errorf("%s report argv asks for %q", kind, forbidden)
				}
			}
		}
		if argv[0] != lvmToolFor[kind] {
			t.Errorf("%s report runs %q", kind, argv[0])
		}
		// The -o list is the exact field set, so a field dropped from the
		// constant is caught rather than silently reading back as "".
		if want := strings.Join(lvmFieldsFor(kind), ","); !strings.Contains(joined, want) {
			t.Errorf("%s report argv %q does not name the fields %q", kind, joined, want)
		}
	}
	// A filter is appended as a bare argument, the way pvs/vgs/lvs take
	// one.
	if got := strings.Join(lvmReportArgv("vg", "vgtest"), " "); !strings.HasSuffix(got, " vgtest") {
		t.Errorf("a filtered vg report is %q", got)
	}
}

// Every mutating command is the one its tool takes, pinned from any host.
//
// This is plan.md §1.4's lesson applied before it costs anything: a
// decision made from an argument is checkable everywhere, and an
// argument vector written for a tool that is on no machine here is not.
func TestLVMArgVectorsAreTheOnesTheToolsTake(t *testing.T) {
	cases := []struct {
		name string
		got  []string
		want string
	}{
		{"pvcreate", lvmPVCreateArgv([]string{"/dev/loop0", "/dev/loop1"}, false),
			"pvcreate /dev/loop0 /dev/loop1"},
		{"pvcreate force", lvmPVCreateArgv([]string{"/dev/sdb"}, true),
			"pvcreate -ff -y /dev/sdb"},
		{"pvremove", lvmPVRemoveArgv([]string{"/dev/loop0"}, false), "pvremove /dev/loop0"},
		{"vgcreate", lvmVGCreateArgv("vgtest", []string{"/dev/loop0"}, "", false),
			"vgcreate vgtest /dev/loop0"},
		{"vgcreate with extent size", lvmVGCreateArgv("vgtest", []string{"/dev/loop0"}, "16M", true),
			"vgcreate -f -s 16M vgtest /dev/loop0"},
		{"vgextend", lvmVGExtendArgv("vgtest", []string{"/dev/loop1"}, false),
			"vgextend vgtest /dev/loop1"},
		{"vgremove", lvmVGRemoveArgv("vgtest", false), "vgremove vgtest"},
		{"vgremove force", lvmVGRemoveArgv("vgtest", true), "vgremove -f vgtest"},
		{"lvremove", lvmLVRemoveArgv("vgtest/data", true), "lvremove -f vgtest/data"},
	}
	for _, c := range cases {
		if got := strings.Join(c.got, " "); got != c.want {
			t.Errorf("%s runs\n  %s\nwant\n  %s", c.name, got, c.want)
		}
	}
}

// lvcreate has the most shapes, so it has its own table.
func TestLVMLVCreateArgvShapes(t *testing.T) {
	ok := []struct {
		name string
		opts lvmLVCreateOpts
		want string
	}{
		{"plain, absolute size",
			lvmLVCreateOpts{Name: "data", Group: "vgtest", Size: "10G"},
			"lvcreate -n data -L 10G vgtest"},
		{"plain, extents",
			lvmLVCreateOpts{Name: "data", Group: "vgtest", Extents: "50%FREE"},
			"lvcreate -n data -l 50%FREE vgtest"},
		{"striped",
			lvmLVCreateOpts{Name: "data", Group: "vgtest", Size: "1G", Stripes: 2, Force: true},
			"lvcreate -n data -y -i 2 -L 1G vgtest"},
		{"thin pool",
			lvmLVCreateOpts{Name: "pool0", Group: "vgtest", Size: "20G", ThinPoolCreate: true},
			"lvcreate -n pool0 --type thin-pool -L 20G vgtest"},
		{"thin volume in a pool",
			lvmLVCreateOpts{Name: "data", Group: "vgtest", Size: "100G", ThinPool: "pool0"},
			"lvcreate -n data -V 100G --thinpool pool0 vgtest"},
	}
	for _, c := range ok {
		argv, err := lvmLVCreateArgv(c.opts)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := strings.Join(argv, " "); got != c.want {
			t.Errorf("%s runs\n  %s\nwant\n  %s", c.name, got, c.want)
		}
	}

	bad := []struct {
		name string
		opts lvmLVCreateOpts
	}{
		{"no size at all", lvmLVCreateOpts{Name: "data", Group: "vgtest"}},
		{"size and extents together",
			lvmLVCreateOpts{Name: "data", Group: "vgtest", Size: "1G", Extents: "10"}},
		{"a slash in the name", lvmLVCreateOpts{Name: "vgtest/data", Group: "vgtest", Size: "1G"}},
		{"thin volume with no virtual size",
			lvmLVCreateOpts{Name: "data", Group: "vgtest", ThinPool: "pool0"}},
		{"no group", lvmLVCreateOpts{Name: "data", Size: "1G"}},
	}
	for _, c := range bad {
		if _, err := lvmLVCreateArgv(c.opts); err == nil {
			t.Errorf("%s was accepted", c.name)
		}
	}
}

// lvresize needs exactly one of size and extents, and the -f that allows
// a shrink is only there when a call asked for it.
func TestLVMLVResizeArgv(t *testing.T) {
	if _, err := lvmLVResizeArgv("vgtest/data", "", "", false, false); err == nil {
		t.Error("lvresize with no size was accepted")
	}
	if _, err := lvmLVResizeArgv("vgtest/data", "10G", "100%VG", false, false); err == nil {
		t.Error("lvresize with both size and extents was accepted")
	}
	argv, _ := lvmLVResizeArgv("vgtest/data", "10G", "", true, false)
	if got := strings.Join(argv, " "); got != "lvresize --resizefs -L 10G vgtest/data" {
		t.Errorf("a grow with a filesystem runs %q", got)
	}
	argv, _ = lvmLVResizeArgv("vgtest/data", "5G", "", false, true)
	if got := strings.Join(argv, " "); got != "lvresize -f -L 5G vgtest/data" {
		t.Errorf("a forced shrink runs %q", got)
	}
}

// The two spellings of a logical volume resolve to the one LVM wants,
// and a contradiction between them is refused.
func TestLVMLVRefResolvesBothSpellings(t *testing.T) {
	for _, c := range []struct {
		name, vgname string
		wantRef      string
	}{
		{"vgtest/data", "", "vgtest/data"},
		{"data", "vgtest", "vgtest/data"},
		{"vgtest/data", "vgtest", "vgtest/data"},
	} {
		_, _, ref, err := lvmLVRef(c.name, c.vgname)
		if err != nil || ref != c.wantRef {
			t.Errorf("lvmLVRef(%q, %q) = %q, %v; want %q", c.name, c.vgname, ref, err, c.wantRef)
		}
	}
	for _, c := range []struct{ name, vgname string }{
		{"data", ""},           // no group anywhere
		{"vgtest/data", "vg9"}, // the two disagree
		{"", "vgtest"},         // nothing named
		{"vgtest/", "vgtest"},  // half a reference
	} {
		if _, _, _, err := lvmLVRef(c.name, c.vgname); err == nil {
			t.Errorf("lvmLVRef(%q, %q) was accepted", c.name, c.vgname)
		}
	}
}

// A size is read in binary units, and a relative or percentage size is
// reported as one the comparison cannot judge.
func TestLVMParseSize(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int64
	}{
		{"1G", 1 << 30},
		{"512M", 512 << 20},
		{"1g", 1 << 30},
		{"2T", 2 << 40},
		{"100", 100 << 20}, // -L's default unit is MiB
		{"4096b", 4096},
		{"2s", 1024}, // a sector is 512 bytes
	} {
		got, ok := lvmParseSize(c.in)
		if !ok || got != c.want {
			t.Errorf("lvmParseSize(%q) = %d, %v; want %d", c.in, got, ok, c.want)
		}
	}
	for _, in := range []string{"", "+2G", "-1G", "100%FREE", "50%VG", "banana", "1Q"} {
		if _, ok := lvmParseSize(in); ok {
			t.Errorf("lvmParseSize(%q) was treated as an absolute size", in)
		}
	}
}

// ---- behaviour, on a scripted runner so it runs on any host ----

// lvmTestContext is a context whose tools are all "present" and whose
// commands are answered from a table, so the module's logic can be
// exercised where there is no LVM.
func lvmTestContext(responses map[string]exec.Result) *exec.Context {
	return &exec.Context{
		Runner: &exec.RecordingRunner{Responses: responses},
		Lookup: func(name string) string { return "/usr/sbin/" + name },
	}
}

func lvmPVReportKey() string {
	return (exec.Command{Argv: lvmReportArgv("pv", "")}).String()
}

func lvmLVReportKey() string {
	return (exec.Command{Argv: lvmReportArgv("lv", "")}).String()
}

// pvcreate skips a device that already carries a label, and reports no
// change when they all do.
func TestLVMPVCreateIsIdempotent(t *testing.T) {
	responses := map[string]exec.Result{lvmPVReportKey(): {Stdout: lvmPVsJSON}}
	c := lvmTestContext(responses)

	// /dev/loop0 is already a PV in the fixture; /dev/loop1 is a PV too
	// but free. A third device is not a PV at all.
	args := value.NewMap(1)
	args.Set("devices", []any{"/dev/loop0", "/dev/loop2"})
	out, err := lvmPVCreate(c, args)
	if err != nil {
		t.Fatalf("pvcreate: %v", err)
	}
	m := out.(*value.Map)
	if changed, _ := m.GetString("changed"); changed != true {
		t.Errorf("pvcreate over a mix of present and absent reported no change: %v", m)
	}
	// It is a test-mode-free real run against a scripted runner; the
	// pvcreate it ran should name only the device that needed it.
	var ran []string
	for _, cmd := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.HasPrefix(cmd, "pvcreate") {
			ran = append(ran, cmd)
		}
	}
	if len(ran) != 1 || !strings.Contains(ran[0], "/dev/loop2") || strings.Contains(ran[0], "/dev/loop0") {
		t.Errorf("pvcreate ran %v; want it to label only /dev/loop2", ran)
	}

	// And when every named device is already a PV, nothing runs.
	c2 := lvmTestContext(responses)
	args2 := value.NewMap(1)
	args2.Set("devices", []any{"/dev/loop0", "/dev/loop1"})
	out2, err := lvmPVCreate(c2, args2)
	if err != nil {
		t.Fatalf("second pvcreate: %v", err)
	}
	if changed, _ := out2.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("pvcreate over devices that are all PVs reported a change: %v", out2)
	}
	for _, cmd := range c2.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.HasPrefix(cmd, "pvcreate") {
			t.Errorf("pvcreate ran %q when there was nothing to do", cmd)
		}
	}
}

// A test-mode mutation predicts its change and runs nothing that alters
// the machine.
func TestLVMPVCreateTestModeChangesNothing(t *testing.T) {
	c := lvmTestContext(map[string]exec.Result{lvmPVReportKey(): {Stdout: lvmPVsJSON}})
	c.Test = true
	args := value.NewMap(1)
	args.Set("devices", []any{"/dev/loop2"})
	out, err := lvmPVCreate(c, args)
	if err != nil {
		t.Fatal(err)
	}
	if res := out.(*value.Map); func() bool { v, _ := res.GetString("changed"); return v != true }() {
		t.Errorf("test mode did not predict the change: %v", out)
	}
	for _, cmd := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.HasPrefix(cmd, "pvcreate") {
			t.Errorf("a test run executed %q", cmd)
		}
	}
}

// A shrink is refused before LVM is called, unless force is set and a
// smaller absolute size was named on purpose.
func TestLVMResizeRefusesAShrinkWithoutForce(t *testing.T) {
	// The fixture's data volume is 25165824 bytes (24 MiB).
	responses := map[string]exec.Result{lvmLVReportKey(): {Stdout: lvmLVsJSON}}

	c := lvmTestContext(responses)
	shrink := value.NewMap(2)
	shrink.Set("name", "vgtest/data")
	shrink.Set("size", "8M")
	if _, err := lvmLVResize(c, shrink); err == nil {
		t.Fatal("a shrink was accepted without force")
	} else if !strings.Contains(err.Error(), "shrink") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
	for _, cmd := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.HasPrefix(cmd, "lvresize") {
			t.Errorf("lvresize ran %q despite the shrink guard", cmd)
		}
	}

	// With force and a smaller size it goes ahead; in test mode it
	// reports the change and still runs nothing.
	c2 := lvmTestContext(responses)
	c2.Test = true
	forced := value.NewMap(3)
	forced.Set("name", "vgtest/data")
	forced.Set("size", "8M")
	forced.Set("force", true)
	out, err := lvmLVResize(c2, forced)
	if err != nil {
		t.Fatalf("a forced shrink in test mode: %v", err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("a forced shrink predicted no change: %v", out)
	}

	// A grow needs no force.
	c3 := lvmTestContext(responses)
	c3.Test = true
	grow := value.NewMap(2)
	grow.Set("name", "vgtest/data")
	grow.Set("size", "64M")
	if _, err := lvmLVResize(c3, grow); err != nil {
		t.Errorf("a grow was refused: %v", err)
	}

	// The same absolute size is a no-op, not a resize.
	c4 := lvmTestContext(responses)
	same := value.NewMap(2)
	same.Set("name", "vgtest/data")
	same.Set("size", "24M")
	out, err = lvmLVResize(c4, same)
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("resizing to the current size reported a change: %v", out)
	}
}

// lvm.lv_present grows a volume but never shrinks one.
func TestLVMLVPresentStateGrowsButNeverShrinks(t *testing.T) {
	responses := map[string]exec.Result{
		lvmLVReportKey(): {Stdout: lvmLVsJSON},
		(exec.Command{Argv: lvmReportArgv("vg", "")}).String(): {Stdout: lvmVGsJSON},
	}

	// Smaller than the current 24 MiB: satisfied, with a warning.
	c := lvmTestContext(responses)
	args := value.NewMap(3)
	args.Set("name", "data")
	args.Set("vgname", "vgtest")
	args.Set("size", "8M")
	res, err := lvmLVPresentState(c, args)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() || res.HasChanges() {
		t.Errorf("a smaller declared size should be a satisfied no-op: %+v", res)
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, " "), "never shrinks") {
		t.Errorf("a smaller declared size should warn: %v", res.Warnings)
	}

	// Larger: a grow, predicted in test mode.
	c2 := lvmTestContext(responses)
	c2.Test = true
	big := value.NewMap(3)
	big.Set("name", "data")
	big.Set("vgname", "vgtest")
	big.Set("size", "48M")
	res, err = lvmLVPresentState(c2, big)
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != nil {
		t.Errorf("test mode of a grow should return a nil result, got %s", res.ResultString())
	}
	if !res.HasChanges() {
		t.Error("test mode of a grow predicted no change")
	}
}

// A node with no LVM tools is told which package they are in, by name.
func TestLVMRefusesWithoutTheTools(t *testing.T) {
	c := &exec.Context{
		Runner: &exec.RecordingRunner{},
		Lookup: func(string) string { return "" },
	}
	_, err := lvmReport(c, "pv", "")
	if err == nil {
		t.Fatal("lvmReport answered on a node with no pvs")
	}
	if !strings.Contains(err.Error(), "lvm2") {
		t.Errorf("the refusal does not name the package: %v", err)
	}
}

// On a platform the module is not declared for, the registry refuses by
// name — without which every registry-driven assertion here would simply
// not run on that platform.
func TestLVMRefusesOnANonLinuxPlatform(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this is about the platforms lvm is not declared for")
	}
	args := value.NewMap(1)
	args.Set("device", "")
	_, err := New().Exec.Call(&exec.Context{}, "lvm.pvs", args)
	if err == nil {
		t.Fatal("lvm.pvs answered on a non-Linux node")
	}
	if !strings.Contains(err.Error(), "this node is "+runtime.GOOS) {
		t.Errorf("the refusal does not name the platform: %v", err)
	}
}

// vgcreate is idempotent: an existing group is reported, not recreated.
func TestLVMVGCreateIsIdempotent(t *testing.T) {
	responses := map[string]exec.Result{
		(exec.Command{Argv: lvmReportArgv("vg", "")}).String(): {Stdout: lvmVGsJSON},
		lvmPVReportKey(): {Stdout: lvmPVsJSON},
	}
	c := lvmTestContext(responses)
	args := value.NewMap(2)
	args.Set("name", "vgtest")
	args.Set("devices", []any{"/dev/loop0"})
	out, err := lvmVGCreate(c, args)
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("vgcreate on an existing group reported a change: %v", out)
	}
	for _, cmd := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.HasPrefix(cmd, "vgcreate") {
			t.Errorf("vgcreate ran %q for a group that already exists", cmd)
		}
	}
}

// vgextend adds only the devices a group does not already span, and
// refuses one that belongs to a different group.
func TestLVMVGExtendAddsOnlyWhatIsMissing(t *testing.T) {
	responses := map[string]exec.Result{
		(exec.Command{Argv: lvmReportArgv("vg", "")}).String(): {Stdout: lvmVGsJSON},
		lvmPVReportKey(): {Stdout: lvmPVsJSON},
	}
	c := lvmTestContext(responses)
	// /dev/loop0 is already in vgtest; /dev/loop1 is a free PV.
	args := value.NewMap(2)
	args.Set("name", "vgtest")
	args.Set("devices", []any{"/dev/loop0", "/dev/loop1"})
	out, err := lvmVGExtend(c, args)
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("vgextend with a device to add reported no change: %v", out)
	}
	var ran string
	for _, cmd := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.HasPrefix(cmd, "vgextend") {
			ran = cmd
		}
	}
	if ran != "vgextend vgtest /dev/loop1" {
		t.Errorf("vgextend ran %q; want it to add only /dev/loop1", ran)
	}
}
