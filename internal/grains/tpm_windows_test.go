package grains

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// The tpm grain used to answer from whether the TPM driver's service
// key exists. That key exists on every Windows install whether or not a
// TPM is attached, so the grain said true everywhere. It now answers
// from whether the driver is running, which a PnP driver is only once
// its device enumerates (issue 608 item 5, lead L11).
//
// The first version of this test asserted true, because the machine it
// was written on has a TPM 2.0. A windows-2022 runner made it fail, so
// "this host has a TPM" was a fact about one desk, not about Windows.
// The test now asks a second, independent source on whatever host it
// runs on -- the Plug and Play device list, through CIM -- and holds the
// grain to that. It holds on a machine with a TPM and on one without,
// and on one without it is the case the old key-based answer got wrong.
//
// Win32_PnPEntity lists only devices that are present; Service is the
// driver bound to the device, and `TPM` is tpm.sys's service name, the
// one the grain queries. Reading it needs no privilege, which matters
// because the CI legs run as a standard account; Get-Tpm needs an
// administrator.
func TestTPMGrainAgreesWithThePlugAndPlayDeviceList(t *testing.T) {
	g := collect(t, Options{})
	got, ok := g.Get("tpm")
	if !ok {
		t.Fatal("tpm is absent")
	}

	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		`@(Get-CimInstance Win32_PnPEntity -Filter "Service='TPM'" | Where-Object { $_.Status -eq 'OK' }).Count`).CombinedOutput()
	if err != nil {
		t.Fatalf("asking CIM for TPM devices: %v\n%s", err, out)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("CIM's count of TPM devices is not a number: %q", out)
	}
	want := n > 0
	if got != want {
		t.Errorf("tpm = %#v, but Plug and Play lists %d working device(s) bound to the TPM driver, so want %v", got, n, want)
	}
	t.Logf("tpm = %v; Plug and Play lists %d working TPM device(s)", got, n)
}
