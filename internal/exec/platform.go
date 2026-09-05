package exec

// PendingModule is a module SPEC section 15.3 names and this build does
// not have.
type PendingModule struct {
	// Platform is the family it belongs to, so a message can say
	// whether the reader is even on the right machine.
	Platform string
	// When says what it waits on, and is the whole of what an operator
	// is told.
	When string
}

// pendingPlatformModules is SPEC 15.3's inventory less what this build
// registers.
//
// Declared rather than omitted, for the reason the beacons and the
// runners are: a name absent from the registry makes "not written yet"
// and "you have mistyped it" the same message, and the second sends
// somebody looking for a typo that is not there. SPEC 15.3 is an
// inventory of 65 modules; the ones this build ships are absent from
// the table below, and the test named at the end is what keeps that
// true in both directions. A count in this comment would go stale the
// first time a module arrived, so there is not one.
//
// A test holds this table to SPEC 15.3 in both directions, so a module
// that arrives cannot stay listed as pending and one that is added to
// the specification cannot be quietly missed.
var pendingPlatformModules = map[string]PendingModule{
	"aptpkg":               {Platform: "debian", When: "phase 5, with the Debian and Ubuntu platform work"},
	"debconf":              {Platform: "debian", When: "phase 5, with the Debian and Ubuntu platform work"},
	"dpkg":                 {Platform: "debian", When: "phase 5, with the Debian and Ubuntu platform work"},
	"debbuild":             {Platform: "debian", When: "phase 5, with the Debian and Ubuntu platform work"},
	"apt_key":              {Platform: "debian", When: "phase 5, with the Debian and Ubuntu platform work"},
	"ufw":                  {Platform: "debian", When: "phase 5, with the Debian and Ubuntu platform work"},
	"netplan":              {Platform: "debian", When: "phase 5, with the Debian and Ubuntu platform work"},
	"apparmor":             {Platform: "debian", When: "phase 5, with the Debian and Ubuntu platform work"},
	"snap":                 {Platform: "debian", When: "phase 5, with the Debian and Ubuntu platform work"},
	"pro":                  {Platform: "debian", When: "phase 5, with the Debian and Ubuntu platform work"},
	"yumpkg":               {Platform: "rhel", When: "phase 5, with the RHEL platform work"},
	"dnfpkg":               {Platform: "rhel", When: "phase 5, with the RHEL platform work"},
	"rpm":                  {Platform: "rhel", When: "phase 5, with the RHEL platform work"},
	"firewalld":            {Platform: "rhel", When: "phase 5, with the RHEL platform work"},
	"subscription_manager": {Platform: "rhel", When: "phase 5, with the RHEL platform work"},
	"dnf_module":           {Platform: "rhel", When: "phase 5, with the RHEL platform work"},
	"chattr":               {Platform: "rhel", When: "phase 5, with the RHEL platform work"},
	"zypperpkg":            {Platform: "suse", When: "phase 5, with the SUSE platform work"},
	"win_pkg":              {Platform: "windows", When: "phase 5, with Windows parity"},
	"win_file":             {Platform: "windows", When: "phase 5, with Windows parity"},
	"win_useradd":          {Platform: "windows", When: "phase 5, with Windows parity"},
	"win_groupadd":         {Platform: "windows", When: "phase 5, with Windows parity"},
	"win_shadow":           {Platform: "windows", When: "phase 5, with Windows parity"},
	"win_network":          {Platform: "windows", When: "phase 5, with Windows parity"},
	"win_firewall":         {Platform: "windows", When: "phase 5, with Windows parity"},
	"win_disk":             {Platform: "windows", When: "phase 5, with Windows parity"},
	"win_system":           {Platform: "windows", When: "phase 5, with Windows parity"},
	"win_timezone":         {Platform: "windows", When: "phase 5, with Windows parity"},
	"win_wua":              {Platform: "windows", When: "phase 5, with Windows parity"},
	"win_certutil":         {Platform: "windows", When: "phase 5, with Windows parity"},
	"win_dsc":              {Platform: "windows", When: "an extension of kind `module`; SPEC section 24 marks it bridged"},
	"win_lgpo":             {Platform: "windows", When: "an extension of kind `module`; SPEC section 24 marks it bridged"},
	"mac_brew_pkg":         {Platform: "darwin", When: "phase 5, with macOS parity"},
	"mac_service":          {Platform: "darwin", When: "phase 5, with macOS parity"},
	"mac_user":             {Platform: "darwin", When: "phase 5, with macOS parity"},
	"mac_group":            {Platform: "darwin", When: "phase 5, with macOS parity"},
	"mac_shadow":           {Platform: "darwin", When: "phase 5, with macOS parity"},
	"mac_power":            {Platform: "darwin", When: "phase 5, with macOS parity"},
	"mac_softwareupdate":   {Platform: "darwin", When: "phase 5, with macOS parity"},
	"mac_defaults":         {Platform: "darwin", When: "phase 5, with macOS parity"},
	"mac_keychain":         {Platform: "darwin", When: "phase 5, with macOS parity"},
	"mac_assistive":        {Platform: "darwin", When: "phase 5, with macOS parity"},
	"freebsdpkg":           {Platform: "freebsd", When: "phase 5; the development platform, and still not built"},
	"freebsd_service":      {Platform: "freebsd", When: "phase 5; the development platform, and still not built"},
	"freebsd_sysctl":       {Platform: "freebsd", When: "phase 5; the development platform, and still not built"},
	"pf":                   {Platform: "freebsd", When: "phase 5; the development platform, and still not built"},
	"jail":                 {Platform: "freebsd", When: "phase 5; the development platform, and still not built"},
	"systemd_service":      {Platform: "linux", When: "phase 5, with the Linux platform work"},
	"journald":             {Platform: "linux", When: "phase 5, with the Linux platform work"},
	"iptables":             {Platform: "linux", When: "phase 5, with the Linux platform work"},
	"nftables":             {Platform: "linux", When: "phase 5, with the Linux platform work"},
	"lvm":                  {Platform: "linux", When: "phase 5, with the Linux platform work"},
	"mdadm":                {Platform: "linux", When: "phase 5, with the Linux platform work"},
	"quota":                {Platform: "linux", When: "phase 5, with the Linux platform work"},
	"udev":                 {Platform: "linux", When: "phase 5, with the Linux platform work"},
	"modprobe":             {Platform: "linux", When: "phase 5, with the Linux platform work"},
	"pam":                  {Platform: "linux", When: "phase 5, with the Linux platform work"},
	"openssl_cert":         {Platform: "linux", When: "phase 5, with the Linux platform work"},
	"authselect":           {Platform: "linux", When: "phase 5, with the Linux platform work"}}

// PendingPlatform reports why a module SPEC 15.3 names is not in this
// build, and whether it is one of them at all.
func PendingPlatform(module string) (PendingModule, bool) {
	m, ok := pendingPlatformModules[module]
	return m, ok
}

// PendingPlatformModules is every module SPEC 15.3 names that this build
// does not have, for the audit that holds the two together.
func PendingPlatformModules() map[string]PendingModule {
	out := make(map[string]PendingModule, len(pendingPlatformModules))
	for name, m := range pendingPlatformModules {
		out[name] = m
	}
	return out
}
