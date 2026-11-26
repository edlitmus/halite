//go:build windows

package winsvc

// The live half of this package's tests: they create real services in
// the real service control manager, and so they need an elevated token
// and HALITE_SYSTEM_LIVE=1, and skip with the reason otherwise.
//
// The service they create is this test binary. TestMain below looks for
// one argument, and when it is there the process is not a test run at
// all but a service: it speaks the service protocol through
// StartServiceCtrlDispatcher and nothing else. That keeps the service a
// real one -- the manager launches a process, waits for it to report,
// and sends it controls -- without a second binary to build, ship or
// leave behind, and it is the only way to get a service whose pending
// states last as long as a test wants: nothing that ships with Windows
// can be told to sit in START_PENDING for four seconds.
//
// Every service is named with testServicePrefix, which the fleet leg's
// "the machine was put back" step counts afterwards.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	osexec "os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	testServicePrefix = "halitesvctest"
	serviceArg        = "-halite-winsvc-test-service="
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], serviceArg) {
		os.Exit(runTestService(strings.TrimPrefix(os.Args[1], serviceArg)))
	}
	os.Exit(m.Run())
}

// runTestService is the service, run by the manager as its own process.
//
// spec is "name,startMillis,stopMillis": how long it reports
// START_PENDING before RUNNING, and STOP_PENDING before STOPPED. While
// pending it accepts no controls, which is what a real service starting
// up does and what makes ControlService refuse a stop then.
func runTestService(spec string) int {
	parts := strings.Split(spec, ",")
	if len(parts) != 3 {
		return 2
	}
	startMS, err1 := strconv.Atoi(parts[1])
	stopMS, err2 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil {
		return 2
	}
	name16, err := windows.UTF16PtrFromString(parts[0])
	if err != nil {
		return 2
	}

	stop := make(chan struct{}, 1)
	var handle windows.Handle
	var checkpoint uint32
	report := func(state, accepts uint32) {
		st := windows.SERVICE_STATUS{
			ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
			CurrentState:     state,
			ControlsAccepted: accepts,
		}
		if state == windows.SERVICE_START_PENDING || state == windows.SERVICE_STOP_PENDING {
			checkpoint++
			st.CheckPoint = checkpoint
			st.WaitHint = 2000
		}
		_ = windows.SetServiceStatus(handle, &st)
	}
	pend := func(state uint32, d time.Duration) {
		until := time.Now().Add(d)
		for {
			report(state, 0)
			if !time.Now().Before(until) {
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
	}

	handler := windows.NewCallback(func(ctrl, evType, evData, ctx uintptr) uintptr {
		switch uint32(ctrl) {
		case windows.SERVICE_CONTROL_STOP:
			select {
			case stop <- struct{}{}:
			default:
			}
			return 0
		case windows.SERVICE_CONTROL_INTERROGATE:
			return 0
		}
		return uintptr(windows.ERROR_CALL_NOT_IMPLEMENTED)
	})
	serviceMain := windows.NewCallback(func(argc, argv uintptr) uintptr {
		h, err := windows.RegisterServiceCtrlHandlerEx(name16, handler, 0)
		if err != nil {
			return 0
		}
		handle = h
		pend(windows.SERVICE_START_PENDING, time.Duration(startMS)*time.Millisecond)
		report(windows.SERVICE_RUNNING, windows.SERVICE_ACCEPT_STOP)
		<-stop
		pend(windows.SERVICE_STOP_PENDING, time.Duration(stopMS)*time.Millisecond)
		checkpoint = 0
		report(windows.SERVICE_STOPPED, 0)
		return 0
	})
	table := []windows.SERVICE_TABLE_ENTRY{
		{ServiceName: name16, ServiceProc: serviceMain},
		{},
	}
	if err := windows.StartServiceCtrlDispatcher(&table[0]); err != nil {
		return 1
	}
	return 0
}

// requireLive skips, saying why, unless this run may create services.
func requireLive(t *testing.T) {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("HALITE_SYSTEM_LIVE is not 1: this test creates, starts and deletes " +
			"real services in this machine's service control manager")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("this process has no elevated token, and creating a service needs one; " +
			"a member of Administrators without elevation is not enough")
	}
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// testService describes one throwaway service.
type testService struct {
	startDelay, stopDelay time.Duration
	// dependsOn names services this one cannot start without.
	dependsOn []string
}

// createService registers this test binary as a service and arranges
// for it to be stopped and deleted when the test ends -- and checks,
// rather than assumes, that the manager no longer knows it.
//
// This is setup, and it uses the API directly with full access on
// purpose: the code under test must not be what creates the fixture it
// is tested against.
func createService(t *testing.T, spec testService) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := testServicePrefix + randomHex(t, 4)
	bin := fmt.Sprintf(`"%s" %s%s,%d,%d`, exe, serviceArg, name,
		spec.startDelay.Milliseconds(), spec.stopDelay.Milliseconds())

	var deps *uint16
	if len(spec.dependsOn) > 0 {
		// A double-NUL-terminated list, which UTF16FromString cannot
		// build because it stops at the first NUL.
		var u []uint16
		for _, d := range spec.dependsOn {
			w, err := windows.UTF16FromString(d)
			if err != nil {
				t.Fatal(err)
			}
			u = append(u, w...)
		}
		u = append(u, 0)
		deps = &u[0]
	}

	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_ALL_ACCESS)
	if err != nil {
		t.Fatalf("opening the manager to create a test service: %v", err)
	}
	defer windows.CloseServiceHandle(m)
	h, err := windows.CreateService(m, u16(t, name), u16(t, name),
		windows.SERVICE_ALL_ACCESS, windows.SERVICE_WIN32_OWN_PROCESS,
		windows.SERVICE_DEMAND_START, windows.SERVICE_ERROR_NORMAL,
		u16(t, bin), nil, nil, deps, nil, nil)
	if err != nil {
		t.Fatalf("creating the test service %s: %v", name, err)
	}
	windows.CloseServiceHandle(h)
	t.Logf("created service %s", name)
	t.Cleanup(func() { deleteService(t, name) })
	return name
}

func u16(t *testing.T, s string) *uint16 {
	t.Helper()
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// deleteService stops a test service if it is running, deletes it, and
// waits until the manager has really let it go: DeleteService only
// marks a service, and it stays listed while any handle is open.
func deleteService(t *testing.T, name string) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_ALL_ACCESS)
	if err != nil {
		t.Errorf("cleanup: opening the manager: %v", err)
		return
	}
	defer windows.CloseServiceHandle(m)
	h, err := windows.OpenService(m, u16(t, name), windows.SERVICE_ALL_ACCESS)
	if err != nil {
		if err != windows.ERROR_SERVICE_DOES_NOT_EXIST {
			t.Errorf("cleanup: opening %s: %v", name, err)
		}
		return
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		var st windows.SERVICE_STATUS
		if err := windows.QueryServiceStatus(h, &st); err != nil || st.CurrentState == windows.SERVICE_STOPPED {
			break
		}
		// Refused while it is pending or while a dependent runs; the
		// dependent's own cleanup runs first, so asking again is enough.
		_ = windows.ControlService(h, windows.SERVICE_CONTROL_STOP, &st)
		if time.Now().After(deadline) {
			t.Errorf("cleanup: %s did not stop", name)
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := windows.DeleteService(h); err != nil {
		t.Errorf("cleanup: deleting %s: %v", name, err)
	}
	windows.CloseServiceHandle(h)
	for time.Now().Before(deadline) {
		g, err := windows.OpenService(m, u16(t, name), windows.SERVICE_QUERY_STATUS)
		if err == windows.ERROR_SERVICE_DOES_NOT_EXIST {
			t.Logf("deleted service %s", name)
			return
		}
		if err == nil {
			windows.CloseServiceHandle(g)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("cleanup: the manager still knows %s", name)
}

// rawState reads a service's state with the API rather than through
// this package, so an assertion about what winsvc did is not made by
// winsvc.
func rawState(t *testing.T, name string) uint32 {
	t.Helper()
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseServiceHandle(m)
	h, err := windows.OpenService(m, u16(t, name), windows.SERVICE_QUERY_STATUS)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseServiceHandle(h)
	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(h, &st); err != nil {
		t.Fatal(err)
	}
	return st.CurrentState
}

// rawStartType reads a service's start type with the API, for the same
// reason rawState does.
func rawStartType(t *testing.T, name string) uint32 {
	t.Helper()
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseServiceHandle(m)
	h, err := windows.OpenService(m, u16(t, name), windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseServiceHandle(h)
	var needed uint32
	_ = windows.QueryServiceConfig(h, nil, 0, &needed)
	if needed == 0 {
		t.Fatal("the manager reported no configuration")
	}
	buf := make([]byte, needed)
	cfg := (*windows.QUERY_SERVICE_CONFIG)(unsafe.Pointer(&buf[0]))
	if err := windows.QueryServiceConfig(h, cfg, needed, &needed); err != nil {
		t.Fatal(err)
	}
	return cfg.StartType
}

// sc runs sc.exe, which is what an operator would delegate with, and
// what the task this test answers asked to be used.
func sc(t *testing.T, args ...string) string {
	t.Helper()
	out, err := osexec.Command("sc.exe", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("sc.exe %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

var (
	advapi32                    = windows.NewLazySystemDLL("advapi32.dll")
	procLogonUserW              = advapi32.NewProc("LogonUserW")
	procImpersonateLoggedOnUser = advapi32.NewProc("ImpersonateLoggedOnUser")
)

// standardAccount creates a local account that is in Users and nothing
// else, and logs it on. Both are undone when the test ends, and the
// account's absence is checked rather than assumed.
func standardAccount(t *testing.T) (string, windows.Token) {
	t.Helper()
	name := "halitesvc" + randomHex(t, 3)
	// Long, random, and with every character class, so that no local
	// password policy refuses it.
	password := "Hs1!" + randomHex(t, 12)
	if out, err := osexec.Command("net.exe", "user", name, password, "/add", "/y").CombinedOutput(); err != nil {
		t.Fatalf("creating the account %s: %v\n%s", name, err, out)
	}
	t.Logf("created local account %s", name)
	t.Cleanup(func() {
		if out, err := osexec.Command("net.exe", "user", name, "/delete").CombinedOutput(); err != nil {
			t.Errorf("cleanup: deleting the account %s: %v\n%s", name, err, out)
			return
		}
		if _, _, _, err := windows.LookupSID("", name); err == nil {
			t.Errorf("cleanup: the account %s still resolves after deletion", name)
			return
		}
		t.Logf("deleted local account %s", name)
	})

	const logonInteractive, providerDefault = 2, 0
	var token windows.Token
	r, _, err := procLogonUserW.Call(
		uintptr(unsafe.Pointer(u16(t, name))), uintptr(unsafe.Pointer(u16(t, "."))),
		uintptr(unsafe.Pointer(u16(t, password))),
		logonInteractive, providerDefault, uintptr(unsafe.Pointer(&token)))
	if r == 0 {
		t.Fatalf("logging %s on: %v", name, err)
	}
	t.Cleanup(func() { token.Close() })
	if token.IsElevated() || tokenIsAdmin(t, token) {
		t.Fatalf("the account %s is an administrator, and this test needs one that is not", name)
	}
	return name, token
}

func tokenIsAdmin(t *testing.T, token windows.Token) bool {
	t.Helper()
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	// IsMember needs an impersonation token; a primary one from
	// LogonUser is duplicated into one.
	var imp windows.Token
	if err := windows.DuplicateTokenEx(token, windows.TOKEN_QUERY|windows.TOKEN_IMPERSONATE,
		nil, windows.SecurityImpersonation, windows.TokenImpersonation, &imp); err != nil {
		t.Fatal(err)
	}
	defer imp.Close()
	member, err := imp.IsMember(admins)
	if err != nil {
		t.Fatal(err)
	}
	return member
}

// as runs f on one OS thread impersonating token, and puts the thread
// back afterwards. The manager checks access against the caller's
// thread token, so this is that account calling, not this process
// calling on its behalf.
func as(t *testing.T, token windows.Token, f func()) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r, _, err := procImpersonateLoggedOnUser.Call(uintptr(token)); r == 0 {
		t.Fatalf("impersonating: %v", err)
	}
	defer func() {
		if err := windows.RevertToSelf(); err != nil {
			// A thread left impersonating would run the rest of the
			// suite as somebody else; there is no recovering from that.
			panic(fmt.Sprintf("reverting the impersonation: %v", err))
		}
	}()
	f()
}

// grant adds an allow entry for sid to a service's access list with
// sc.exe sdset, which is the delegation an operator would make by hand.
func grant(t *testing.T, service string, sid *windows.SID, rights string) {
	t.Helper()
	sddl := sc(t, "sdshow", service)
	i := strings.Index(sddl, "D:")
	j := strings.Index(sddl[i:], "(")
	if i < 0 || j < 0 {
		t.Fatalf("cannot find the access list in %q", sddl)
	}
	at := i + j
	ace := fmt.Sprintf("(A;;%s;;;%s)", rights, sid.String())
	updated := sddl[:at] + ace + sddl[at:]
	sc(t, "sdset", service, updated)
	t.Logf("granted %s on %s: %s", rights, service, ace)
}

// A non-administrator granted start, stop and configure on one service,
// by that service's own access list, can use them.
//
// That is what the access list is for: Windows has delegated service
// control this way since NT, and it is how an operator lets a service
// account restart the one thing it owns. It worked through sc.exe and
// never through this package, because every write opened the manager
// asking for SC_MANAGER_ALL_ACCESS -- the right to create and lock
// services -- which only an administrator has, and which none of
// these calls uses (issue 608 item 5, lead L23).
func TestLiveADelegatedAccountCanUseItsRightsOnOneService(t *testing.T) {
	requireLive(t)
	delegated := createService(t, testService{})
	other := createService(t, testService{})
	account, token := standardAccount(t)
	sid, _, _, err := windows.LookupSID("", account)
	if err != nil {
		t.Fatal(err)
	}
	// RP start, WP stop, DC change config, LC query status, CC query
	// config, LO interrogate, RC read the descriptor, SW enumerate
	// dependents.
	grant(t, delegated, sid, "RPWPDCLCCCLOSWRC")

	as(t, token, func() {
		if err := Start(delegated); err != nil {
			t.Errorf("Start as %s: %v", account, err)
		}
	})
	if got := rawState(t, delegated); got != windows.SERVICE_RUNNING {
		t.Errorf("after the delegated Start the service is %s", stateName(got))
	}

	as(t, token, func() {
		if err := SetStartType(delegated, "disabled"); err != nil {
			t.Errorf("SetStartType as %s: %v", account, err)
		}
		if err := Stop(delegated); err != nil {
			t.Errorf("Stop as %s: %v", account, err)
		}
	})
	if got := rawState(t, delegated); got != windows.SERVICE_STOPPED {
		t.Errorf("after the delegated Stop the service is %s", stateName(got))
	}
	if got := rawStartType(t, delegated); got != windows.SERVICE_DISABLED {
		t.Errorf("after the delegated SetStartType the start type is %d, want %d (disabled)",
			got, windows.SERVICE_DISABLED)
	}
	as(t, token, func() {
		if err := SetStartType(delegated, "manual"); err != nil {
			t.Errorf("SetStartType back to manual as %s: %v", account, err)
		}
	})
	if got := rawStartType(t, delegated); got != windows.SERVICE_DEMAND_START {
		t.Errorf("after setting it back the start type is %d, want %d (manual)",
			got, windows.SERVICE_DEMAND_START)
	}

	// The access list is still what decides: the same account on a
	// service that granted it nothing is refused, by the service, and
	// the message does not tell it that the fix is to become an
	// administrator when the fix is a grant.
	as(t, token, func() {
		err := Start(other)
		if err == nil {
			t.Fatalf("%s started a service that granted it nothing", account)
		}
		if !strings.Contains(err.Error(), "opening the service") {
			t.Errorf("the refusal is not the service's own: %v", err)
		}
		if !strings.Contains(err.Error(), "a grant to this account") {
			t.Errorf("the refusal does not say a grant on the service would do: %v", err)
		}
	})
	if got := rawState(t, other); got != windows.SERVICE_STOPPED {
		t.Errorf("the service that granted nothing is %s", stateName(got))
	}
}
