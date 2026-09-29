package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// proIsAttachedSample is a real `pro api u.pro.status.is_attached.v1`
// response, captured from this build's own Ubuntu Pro-attached
// development host (client 37.2ubuntu~24.04.1). `contract_remaining_days`
// is the one field trimmed to a shorter, still-real number; nothing else
// is invented.
const proIsAttachedSample = `{"_schema_version": "v1", "data": {"attributes": {"contract_remaining_days": 2912171, "contract_status": "active", "is_attached": true, "is_attached_and_contract_valid": true}, "meta": {"environment_vars": []}, "type": "IsAttached"}, "errors": [], "result": "success", "version": "37.2ubuntu~24.04.1", "warnings": []}`

const proIsAttachedFailureSample = `{"_schema_version": "v1", "errors": [{"message": "This machine is not attached to an Ubuntu Pro subscription.", "message_code": "unattached"}], "result": "failure", "version": "37.2ubuntu~24.04.1", "warnings": []}`

// proStatusSample is a real `pro status --format json`, captured from
// the same host. Account name, account id, contract id and machine id
// are this project's own operator's identifying values, and are replaced
// here with placeholders of the same shape and type -- the structure
// below (every key, every nesting level, every service's field set) is
// the client's real output, not a guess at one. DIVERGENCE 5.31 is
// exactly the mistake a hand-written fixture makes, and this one is
// captured instead.
const proStatusSample = `{
  "_doc": "Content provided in json response is currently considered Experimental and may change",
  "_schema_version": "0.1",
  "account": {
    "created_at": "2025-03-16T16:09:42+00:00",
    "external_account_ids": [],
    "id": "REDACTED-ACCOUNT-ID",
    "name": "redacted@example.com"
  },
  "attached": true,
  "config": {
    "contract_url": "https://contracts.canonical.com",
    "data_dir": "/var/lib/ubuntu-advantage",
    "log_file": "/var/log/ubuntu-advantage.log",
    "log_level": "debug",
    "security_url": "https://ubuntu.com/security",
    "ua_config": {
      "apt_news": true,
      "metering_timer": 14400
    }
  },
  "config_path": "/etc/ubuntu-advantage/uaclient.conf",
  "contract": {
    "created_at": "2025-03-16T16:09:43+00:00",
    "id": "REDACTED-CONTRACT-ID",
    "name": "Ubuntu Pro - free personal subscription",
    "products": ["free"],
    "tech_support_level": "n/a"
  },
  "effective": "2025-03-16T16:09:43+00:00",
  "environment_vars": [],
  "errors": [],
  "execution_details": "No Ubuntu Pro operations are running",
  "execution_status": "inactive",
  "expires": "9999-12-31T00:00:00+00:00",
  "features": {},
  "machine_id": "REDACTED-MACHINE-ID",
  "notices": [],
  "result": "success",
  "services": [
    {
      "available": "yes",
      "blocked_by": [],
      "description": "Expanded Security Maintenance for Applications",
      "description_override": null,
      "entitled": "yes",
      "name": "esm-apps",
      "status": "enabled",
      "status_details": "Ubuntu Pro: ESM Apps is active",
      "warning": null
    },
    {
      "available": "yes",
      "blocked_by": [],
      "description": "Expanded Security Maintenance for Infrastructure",
      "description_override": null,
      "entitled": "yes",
      "name": "esm-infra",
      "status": "enabled",
      "status_details": "Ubuntu Pro: ESM Infra is active",
      "warning": null
    },
    {
      "available": "yes",
      "blocked_by": [],
      "description": "Canonical Livepatch service",
      "description_override": "Current kernel is not covered by livepatch",
      "entitled": "yes",
      "name": "livepatch",
      "status": "warning",
      "status_details": "",
      "warning": {
        "code": "livepatch-kernel-not-supported",
        "message": "The current kernel is not covered by livepatch."
      }
    },
    {
      "available": "yes",
      "blocked_by": [
        {
          "name": "livepatch",
          "reason": "Livepatch does not currently cover the Real-time kernel.",
          "reason_code": "realtime-livepatch-incompatible"
        }
      ],
      "description": "Ubuntu kernel with PREEMPT_RT patches integrated",
      "description_override": null,
      "entitled": "yes",
      "name": "realtime-kernel",
      "status": "disabled",
      "status_details": "Real-time kernel is not configured",
      "warning": null
    },
    {
      "available": "yes",
      "blocked_by": [],
      "description": "Security compliance and audit tools",
      "description_override": null,
      "entitled": "yes",
      "name": "usg",
      "status": "disabled",
      "status_details": "CIS Audit is not configured",
      "warning": null
    }
  ],
  "simulated": false,
  "version": "37.2ubuntu~24.04.1",
  "warnings": []
}`

func proTestContext(responses map[string]exec.Result) *exec.Context {
	return &exec.Context{
		Runner: &exec.RecordingRunner{Responses: responses},
		Lookup: func(name string) string { return "/usr/bin/" + name },
	}
}

func TestProToolPresentNamesTheTool(t *testing.T) {
	c := &exec.Context{Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "" }}
	err := proToolPresent(c)
	if err == nil || !strings.Contains(err.Error(), "pro") || !strings.Contains(err.Error(), "Debian") {
		t.Fatalf("err = %v, want it to name pro and explain Debian does not ship it", err)
	}
}

func TestProVersionReadsTheRealTrimmedString(t *testing.T) {
	key := (exec.Command{Argv: []string{"pro", "--version"}}).String()
	c := proTestContext(map[string]exec.Result{
		key: {Stdout: "37.2ubuntu~24.04.1\n"},
	})
	got, err := proVersionFn(c, value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	if got != "37.2ubuntu~24.04.1" {
		t.Errorf("version = %#v", got)
	}
}

// TestProParseIsAttached pins the parser against the real envelope both
// ways: attached and refused for want of one.
func TestProParseIsAttached(t *testing.T) {
	attached, err := proParseIsAttached(proIsAttachedSample)
	if err != nil {
		t.Fatal(err)
	}
	if !attached {
		t.Error("a real attached response was read as not attached")
	}

	if _, err := proParseIsAttached(proIsAttachedFailureSample); err == nil {
		t.Error("a `result: failure` envelope was not refused")
	}

	if _, err := proParseIsAttached(""); err == nil {
		t.Error("empty output was not refused")
	}
}

func TestProIsAttachedFn(t *testing.T) {
	key := (exec.Command{Argv: []string{"pro", "api", "u.pro.status.is_attached.v1"}}).String()
	c := proTestContext(map[string]exec.Result{key: {Stdout: proIsAttachedSample}})
	got, err := proIsAttachedFn(c, value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	if got != true {
		t.Errorf("is_attached = %#v", got)
	}
}

// TestProStatusFn checks that the real document's shape survives the
// lift into the value model at every level this module's own design
// note relies on: `attached`, the per-service `status` and `entitled`
// strings (not booleans -- the real client prints "yes"/"no"), and a
// `blocked_by` reason nested two levels inside a service.
func TestProStatusFn(t *testing.T) {
	key := (exec.Command{Argv: []string{"pro", "status", "--format", "json"}}).String()
	c := proTestContext(map[string]exec.Result{key: {Stdout: proStatusSample}})
	got, err := proStatusFn(c, value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	doc, ok := got.(*value.Map)
	if !ok {
		t.Fatalf("status = %T, want *value.Map", got)
	}
	if attached, _ := doc.Get("attached"); attached != true {
		t.Errorf("attached = %#v", attached)
	}
	services, _ := doc.Get("services")
	list, ok := services.([]any)
	if !ok || len(list) != 5 {
		t.Fatalf("services = %#v", services)
	}
	esmInfra, ok := list[1].(*value.Map)
	if !ok {
		t.Fatalf("services[1] = %T", list[1])
	}
	if name, _ := esmInfra.Get("name"); name != "esm-infra" {
		t.Errorf("services[1].name = %#v", name)
	}
	if status, _ := esmInfra.Get("status"); status != "enabled" {
		t.Errorf("services[1].status = %#v", status)
	}
	// entitled is the string "yes", not a bool -- the real client's own
	// spelling, which a hand-written fixture would very plausibly have
	// gotten wrong the other way.
	if entitled, _ := esmInfra.Get("entitled"); entitled != "yes" {
		t.Errorf("services[1].entitled = %#v (want the string \"yes\")", entitled)
	}

	realtime, ok := list[3].(*value.Map)
	if !ok {
		t.Fatalf("services[3] = %T", list[3])
	}
	blockedBy, _ := realtime.Get("blocked_by")
	blockers, ok := blockedBy.([]any)
	if !ok || len(blockers) != 1 {
		t.Fatalf("realtime-kernel.blocked_by = %#v", blockedBy)
	}
	blocker, ok := blockers[0].(*value.Map)
	if !ok {
		t.Fatalf("blocked_by[0] = %T", blockers[0])
	}
	if reasonCode, _ := blocker.Get("reason_code"); reasonCode != "realtime-livepatch-incompatible" {
		t.Errorf("blocked_by[0].reason_code = %#v", reasonCode)
	}
}

// ---- the mutating half: argv builders, pinned without a real client ----

func TestProAttachArgv(t *testing.T) {
	got := proAttachArgv(value.MapOf("token", "C1TOKEN"))
	want := []string{"pro", "attach", "--format", "json", "C1TOKEN"}
	if !equalStrings(got, want) {
		t.Errorf("argv = %v, want %v", got, want)
	}

	got = proAttachArgv(value.MapOf("token", "C1TOKEN", "no_auto_enable", true))
	want = []string{"pro", "attach", "--format", "json", "--no-auto-enable", "C1TOKEN"}
	if !equalStrings(got, want) {
		t.Errorf("argv (no_auto_enable) = %v, want %v", got, want)
	}
}

func TestProDetachArgv(t *testing.T) {
	got := proDetachArgv()
	want := []string{"pro", "detach", "--format", "json", "--assume-yes"}
	if !equalStrings(got, want) {
		t.Errorf("argv = %v, want %v", got, want)
	}
}

func TestProEnableArgv(t *testing.T) {
	got := proEnableArgv(value.MapOf("services", []any{"fips", "usg"}))
	want := []string{"pro", "enable", "--format", "json", "--assume-yes", "fips", "usg"}
	if !equalStrings(got, want) {
		t.Errorf("argv = %v, want %v", got, want)
	}

	got = proEnableArgv(value.MapOf(
		"services", []any{"realtime-kernel"},
		"access_only", true, "beta", true, "variant", "intel-ipu6",
	))
	want = []string{"pro", "enable", "--format", "json", "--assume-yes",
		"--access-only", "--beta", "--variant", "intel-ipu6", "realtime-kernel"}
	if !equalStrings(got, want) {
		t.Errorf("argv (options) = %v, want %v", got, want)
	}
}

func TestProDisableArgv(t *testing.T) {
	got := proDisableArgv(value.MapOf("services", []any{"usg"}))
	want := []string{"pro", "disable", "--format", "json", "--assume-yes", "usg"}
	if !equalStrings(got, want) {
		t.Errorf("argv = %v, want %v", got, want)
	}

	got = proDisableArgv(value.MapOf("services", []any{"usg"}, "purge", true))
	want = []string{"pro", "disable", "--format", "json", "--assume-yes", "--purge", "usg"}
	if !equalStrings(got, want) {
		t.Errorf("argv (purge) = %v, want %v", got, want)
	}
}

// TestProRunChecksTheCommonEnvelope exercises proRun's own logic against
// the minimal shape every captured response above actually carries --
// `result` and `errors` -- rather than against enable/disable's full
// output, which nothing here has seen. It is a test of this module's
// parsing, not a claim about what `pro enable` prints.
func TestProRunChecksTheCommonEnvelope(t *testing.T) {
	argv := []string{"pro", "enable", "--format", "json", "--assume-yes", "usg"}
	key := (exec.Command{Argv: argv}).String()

	c := proTestContext(map[string]exec.Result{
		key: {Stdout: `{"result": "success", "errors": []}`},
	})
	got, err := proRun(c, argv)
	if err != nil {
		t.Fatal(err)
	}
	doc, ok := got.(*value.Map)
	if !ok {
		t.Fatalf("result = %T", got)
	}
	if result, _ := doc.Get("result"); result != "success" {
		t.Errorf("result = %#v", result)
	}

	c = proTestContext(map[string]exec.Result{
		key: {Stdout: `{"result": "failure", "errors": [{"message": "usg is not entitled"}]}`, Code: 1},
	})
	if _, err := proRun(c, argv); err == nil || !strings.Contains(err.Error(), "usg is not entitled") {
		t.Errorf("err = %v, want it to carry the client's own message", err)
	}

	c = proTestContext(map[string]exec.Result{key: {Stdout: "", Code: 1}})
	if _, err := proRun(c, argv); err == nil {
		t.Error("empty output with a non-zero exit was not refused")
	}
}
