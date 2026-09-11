package builtin

import (
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// A real `mdadm --detail` of a RAID1 with one spare, mid-resync.
const mdadmDetailSample = `/dev/md/haltest:
           Version : 1.2
     Creation Time : Thu Sep 10 15:48:34 2026
        Raid Level : raid1
        Array Size : 64512 (63.00 MiB 66.06 MB)
     Used Dev Size : 64512 (63.00 MiB 66.06 MB)
      Raid Devices : 2
     Total Devices : 3
       Persistence : Superblock is persistent

       Update Time : Thu Sep 10 15:48:34 2026
             State : clean, resyncing
    Active Devices : 2
   Working Devices : 3
    Failed Devices : 0
     Spare Devices : 1

Consistency Policy : resync

     Resync Status : 27% complete

              Name : system76-pc:haltest  (local to host system76-pc)
              UUID : a7c391aa:e9796964:1cb84fce:57ea8d28
            Events : 4

    Number   Major   Minor   RaidDevice State
       0       7        4        0      active sync   /dev/loop4
       1       7        7        1      active sync   /dev/loop7

       2       7        8        -      spare   /dev/loop8
`

// A degraded array: one slot removed, one member faulty.
const mdadmDetailDegraded = `/dev/md127:
        Raid Level : raid1
      Raid Devices : 2
     Total Devices : 2
             State : clean, degraded
    Active Devices : 1
   Working Devices : 1
    Failed Devices : 1
     Spare Devices : 0
              UUID : deadbeef:deadbeef:deadbeef:deadbeef

    Number   Major   Minor   RaidDevice State
       -       0        0        0      removed
       1       7        7        1      active sync   /dev/loop7

       0       7        4        -      faulty   /dev/loop4
`

const mdadmMdstatSample = `Personalities : [raid0] [raid1]
md127 : active raid1 loop8[2](S) loop7[1] loop4[0]
      64512 blocks super 1.2 [2/1] [_U]
      [===>.................]  recovery = 18.8% (12288/64512) finish=0.0min speed=12288K/sec

md0 : active raid0 sdb[1] sda[0]
      2097152 blocks super 1.2 512k chunks

unused devices: <none>
`

func TestMdadmParsesDetail(t *testing.T) {
	a := mdadmParseDetail(mdadmDetailSample)
	if a.Level != "raid1" {
		t.Errorf("level = %q", a.Level)
	}
	if a.State != "clean, resyncing" {
		t.Errorf("state = %q", a.State)
	}
	if a.Name != "system76-pc:haltest" {
		t.Errorf("name = %q (the parenthetical should be stripped)", a.Name)
	}
	if a.UUID != "a7c391aa:e9796964:1cb84fce:57ea8d28" {
		t.Errorf("uuid = %q", a.UUID)
	}
	if a.RaidDevices != 2 || a.TotalDevices != 3 || a.SpareDevices != 1 {
		t.Errorf("counts: raid=%d total=%d spare=%d", a.RaidDevices, a.TotalDevices, a.SpareDevices)
	}
	if a.ArraySizeKB != 64512 {
		t.Errorf("array size kb = %d", a.ArraySizeKB)
	}
	if a.ResyncAction != "resync" || !strings.Contains(a.ResyncPct, "27%") {
		t.Errorf("resync: action=%q pct=%q", a.ResyncAction, a.ResyncPct)
	}
	if len(a.Members) != 3 {
		t.Fatalf("parsed %d members, want 3: %+v", len(a.Members), a.Members)
	}
	if a.Members[0].Device != "/dev/loop4" || a.Members[0].State != "active sync" || a.Members[0].RaidDevice != "0" {
		t.Errorf("member 0 = %+v", a.Members[0])
	}
	if a.Members[2].Device != "/dev/loop8" || a.Members[2].State != "spare" || a.Members[2].RaidDevice != "-" {
		t.Errorf("spare member = %+v", a.Members[2])
	}
}

func TestMdadmParsesDetailDegraded(t *testing.T) {
	a := mdadmParseDetail(mdadmDetailDegraded)
	if a.State != "clean, degraded" {
		t.Errorf("state = %q", a.State)
	}
	v := mdadmDetailValue(a)
	if deg, _ := v.GetString("degraded"); deg != true {
		t.Error("a 'clean, degraded' array is not reported as degraded")
	}
	if len(a.Members) != 3 {
		t.Fatalf("parsed %d members, want 3 (removed + active + faulty)", len(a.Members))
	}
	// The `removed` slot has no device path.
	var removed, faulty *mdadmMemberInfo
	for i := range a.Members {
		switch {
		case a.Members[i].State == "removed":
			removed = &a.Members[i]
		case strings.Contains(a.Members[i].State, "faulty"):
			faulty = &a.Members[i]
		}
	}
	if removed == nil || removed.Device != "" {
		t.Errorf("removed slot = %+v", removed)
	}
	if faulty == nil || faulty.Device != "/dev/loop4" {
		t.Errorf("faulty member = %+v", faulty)
	}
}

func TestMdadmParsesMdstat(t *testing.T) {
	arrays := mdadmParseMdstat(mdadmMdstatSample)
	if len(arrays) != 2 {
		t.Fatalf("parsed %d arrays, want 2", len(arrays))
	}
	md127 := arrays[0]
	if md127.Device != "/dev/md127" || md127.State != "active" || md127.Personality != "raid1" {
		t.Errorf("md127 = %+v", md127)
	}
	if len(md127.Members) != 3 {
		t.Fatalf("md127 has %d members, want 3", len(md127.Members))
	}
	spare := mdadmMemberSpecValue(md127.Members[0])
	if n, _ := spare.GetString("name"); n != "loop8" {
		t.Errorf("first member name = %v", n)
	}
	if s, _ := spare.GetString("spare"); s != true {
		t.Errorf("loop8[2](S) is not reported as a spare: %v", spare)
	}
	total, active, ok := mdadmParseUpCount(md127.BlocksLine)
	if !ok || total != 2 || active != 1 {
		t.Errorf("up count = %d/%d (ok=%v), want 2/1", active, total, ok)
	}
	if !strings.Contains(md127.Progress, "18.8%") {
		t.Errorf("progress = %q", md127.Progress)
	}

	// The RAID0 has no redundancy and no progress line.
	md0 := arrays[1]
	if md0.Personality != "raid0" || md0.Progress != "" {
		t.Errorf("md0 = %+v", md0)
	}
}

func TestMdadmCreateArgv(t *testing.T) {
	argv, err := mdadmCreateArgv(mdadmCreateOpts{
		Device: "/dev/md0", Level: "1",
		Devices:  []string{"/dev/sda", "/dev/sdb"},
		Spares:   []string{"/dev/sdc"},
		Metadata: "1.2", Name: "data",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "mdadm --create /dev/md0 --run --level=1 --raid-devices=2 --spare-devices=1 --metadata=1.2 --name=data /dev/sda /dev/sdb /dev/sdc"
	if got := strings.Join(argv, " "); got != want {
		t.Errorf("create argv =\n  %s\nwant\n  %s", got, want)
	}
	if _, err := mdadmCreateArgv(mdadmCreateOpts{Device: "/dev/md0", Devices: []string{"/dev/sda"}}); err == nil {
		t.Error("create with no level was accepted")
	}
	if _, err := mdadmCreateArgv(mdadmCreateOpts{Device: "/dev/md0", Level: "1"}); err == nil {
		t.Error("create with no devices was accepted")
	}
}

func TestMdadmGrowArgv(t *testing.T) {
	argv, _ := mdadmGrowArgv("/dev/md0", 3, "")
	if got := strings.Join(argv, " "); got != "mdadm --grow /dev/md0 --raid-devices=3" {
		t.Errorf("grow argv = %q", got)
	}
	argv, _ = mdadmGrowArgv("/dev/md0", 0, "max")
	if got := strings.Join(argv, " "); got != "mdadm --grow /dev/md0 --size=max" {
		t.Errorf("grow --size argv = %q", got)
	}
	if _, err := mdadmGrowArgv("/dev/md0", 0, ""); err == nil {
		t.Error("grow with nothing to do was accepted")
	}
}

func TestMdadmLevelName(t *testing.T) {
	for in, want := range map[string]string{"1": "raid1", "10": "raid10", "raid5": "raid5", "": ""} {
		if got := mdadmLevelName(in); got != want {
			t.Errorf("mdadmLevelName(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- behaviour, scripted runner ----

func mdadmCtx(responses map[string]exec.Result) *exec.Context {
	return &exec.Context{
		Runner: &exec.RecordingRunner{Responses: responses},
		Lookup: func(name string) string { return "/usr/sbin/" + name },
	}
}

func TestMdadmCreateRefusesAnExistingArray(t *testing.T) {
	detail := (exec.Command{Argv: []string{"mdadm", "--detail", "/dev/md0"}}).String()
	c := mdadmCtx(map[string]exec.Result{detail: {Stdout: mdadmDetailSample}})
	out, err := mdadmCreateFn(c, value.MapOf(
		"device", "/dev/md0", "level", "1", "devices", []any{"/dev/sda", "/dev/sdb"}))
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("create over an existing array reported a change: %v", out)
	}
	for _, ran := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.Contains(ran, "--create") {
			t.Errorf("create ran %q for an array that already exists", ran)
		}
	}
}

func TestMdadmCreateRefusesAMemberWithASuperblock(t *testing.T) {
	detail := (exec.Command{Argv: []string{"mdadm", "--detail", "/dev/md0"}}).String()
	examine := (exec.Command{Argv: []string{"mdadm", "--examine", "--export", "/dev/sda"}}).String()
	c := mdadmCtx(map[string]exec.Result{
		detail:  {Code: 1, Stderr: "mdadm: cannot open /dev/md0: No such file or directory"},
		examine: {Stdout: "MD_LEVEL=raid1\nMD_UUID=x\n"},
	})
	_, err := mdadmCreateFn(c, value.MapOf(
		"device", "/dev/md0", "level", "1", "devices", []any{"/dev/sda", "/dev/sdb"}))
	if err == nil {
		t.Fatal("create over a device with an md superblock was allowed without force")
	}
	if !strings.Contains(err.Error(), "md superblock") {
		t.Errorf("the refusal does not explain: %v", err)
	}

	// With force it goes ahead (test mode).
	c2 := mdadmCtx(map[string]exec.Result{
		detail:  {Code: 1, Stderr: "mdadm: cannot open /dev/md0: No such file or directory"},
		examine: {Stdout: "MD_LEVEL=raid1\n"},
	})
	c2.Test = true
	out, err := mdadmCreateFn(c2, value.MapOf(
		"device", "/dev/md0", "level", "1", "devices", []any{"/dev/sda", "/dev/sdb"}, "force", true))
	if err != nil {
		t.Fatalf("forced create: %v", err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("a forced create in test mode predicted no change: %v", out)
	}
}

func TestMdadmStopIsIdempotent(t *testing.T) {
	detail := (exec.Command{Argv: []string{"mdadm", "--detail", "/dev/md9"}}).String()
	c := mdadmCtx(map[string]exec.Result{
		detail: {Code: 1, Stderr: "mdadm: cannot open /dev/md9: No such file or directory"},
	})
	out, err := mdadmStopFn(c, value.MapOf("device", "/dev/md9"))
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("stopping a non-running array reported a change: %v", out)
	}
	for _, ran := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.Contains(ran, "--stop") {
			t.Errorf("stop ran %q against an array that was not running", ran)
		}
	}
}

func TestMdadmRefusesWithoutTheTool(t *testing.T) {
	c := &exec.Context{Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "" }}
	if _, err := mdadmList(c); err == nil || !strings.Contains(err.Error(), "mdadm") {
		t.Errorf("the refusal does not name the package: %v", err)
	}
	if _, err := mdadmDetail(c, "/dev/md0"); err == nil || !strings.Contains(err.Error(), "mdadm") {
		t.Errorf("detail's refusal does not name the package: %v", err)
	}
}

func TestMdadmRefusesOnANonLinuxPlatform(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this is about the platforms mdadm is not declared for")
	}
	_, err := New().Exec.Call(&exec.Context{}, "mdadm.list", value.NewMap(0))
	if err == nil || !strings.Contains(err.Error(), "this node is "+runtime.GOOS) {
		t.Errorf("mdadm.list did not refuse by platform: %v", err)
	}
}
