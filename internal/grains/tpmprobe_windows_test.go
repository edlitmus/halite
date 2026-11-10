package grains

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows/registry"

	"github.com/edlitmus/halite/internal/winsvc"
)

// TEMPORARY probe for PR 683: what does a windows-2022 runner say about
// its TPM? Fails on purpose so the output is shown. Removed before merge.
func TestProbeTPMFacts(t *testing.T) {
	run, err := winsvc.Running("TPM")
	t.Logf("winsvc.Running(TPM) = %v, %v", run, err)
	s, err := winsvc.Query("TPM")
	t.Logf("winsvc.Query(TPM) = %+v, %v", s, err)
	k, kerr := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\TPM`, registry.QUERY_VALUE)
	if kerr == nil {
		k.Close()
	}
	t.Logf("old answer (service key exists) = %v, %v", kerr == nil, kerr)
	for _, c := range [][]string{
		{"sc.exe", "query", "TPM"},
		{"sc.exe", "qc", "TPM"},
		{"whoami"},
		{"powershell", "-NoProfile", "-Command", "Get-CimInstance Win32_PnPEntity -Filter \"Service='TPM'\" | Format-List Name,Status,Present,DeviceID"},
		{"powershell", "-NoProfile", "-Command", "Get-PnpDevice -Class SecurityDevices | Format-List FriendlyName,Status,Present,InstanceId,Problem"},
		{"powershell", "-NoProfile", "-Command", "Get-PnpDevice -PresentOnly | Where-Object { $_.Service -eq 'TPM' -or $_.FriendlyName -like '*Trusted Platform*' } | Format-List FriendlyName,Status,Service,InstanceId"},
		{"powershell", "-NoProfile", "-Command", "Get-Tpm"},
		{"powershell", "-NoProfile", "-Command", "Get-CimInstance Win32_ComputerSystem | Format-List Manufacturer,Model; Get-CimInstance Win32_OperatingSystem | Format-List Caption,Version"},
		{"powershell", "-NoProfile", "-Command", "Get-CimInstance -Namespace root/cimv2/Security/MicrosoftTpm -ClassName Win32_Tpm | Format-List *"},
	} {
		out, err := exec.Command(c[0], c[1:]...).CombinedOutput()
		t.Logf("$ %v (err %v)\n%s", c, err, out)
	}
	t.Error("probe: fails on purpose so the facts above are printed")
}
