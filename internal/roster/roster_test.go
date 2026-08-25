package roster

import (
	"strings"
	"testing"
	"time"
)

func TestAFlatRosterReadsItsFields(t *testing.T) {
	src := `
web1.example:
  host: 10.0.0.4
  port: 2222
  user: deploy
  priv: /home/ed/.ssh/estate
  sudo: true
  sudo_user: root
  timeout: 45
  thin_dir: /var/tmp/halite
  identities_only: true
  proxy_jump: bastion.example
  grains:
    role: web
    dc: sfo
db1.example: 10.0.0.9
lonely.example:
`
	r, err := ParseFlat([]byte(src), "roster")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Targets) != 3 {
		t.Fatalf("it read %d targets", len(r.Targets))
	}

	web, ok := r.Get("web1.example")
	if !ok {
		t.Fatal("web1.example is missing")
	}
	if web.Host != "10.0.0.4" || web.Port != 2222 || web.User != "deploy" {
		t.Errorf("the connection is %+v", web)
	}
	if !web.Sudo || web.SudoUser != "root" || !web.IdentitiesOnly {
		t.Errorf("the flags are %+v", web)
	}
	if web.Timeout != 45*time.Second {
		t.Errorf("the timeout is %s; a roster spells it in bare seconds", web.Timeout)
	}
	if web.ProxyJump != "bastion.example" {
		t.Errorf("proxy_jump is %q", web.ProxyJump)
	}
	if web.Grains == nil {
		t.Error("the grains were dropped")
	}

	// The shorthand: a bare string is the host.
	db, _ := r.Get("db1.example")
	if db.Host != "10.0.0.9" {
		t.Errorf("the shorthand read as %q", db.Host)
	}
	// And a target with nothing under it connects by its own name.
	lonely, _ := r.Get("lonely.example")
	if lonely.Host != "lonely.example" {
		t.Errorf("an empty target reads as %q", lonely.Host)
	}
	if lonely.Port != 22 || lonely.ThinDir == "" {
		t.Errorf("the defaults did not apply: %+v", lonely)
	}
}

// A misspelt roster field is a setting that does nothing — `sudo_usr`
// leaves the run as root — and this project refuses those everywhere.
func TestAMisspeltRosterFieldIsRefused(t *testing.T) {
	_, err := ParseFlat([]byte("web1:\n  sudo_usr: deploy\n"), "roster")
	if err == nil {
		t.Fatal("a misspelt field was accepted")
	}
	if !strings.Contains(err.Error(), "sudo_user") {
		t.Errorf("the refusal does not list the real fields: %v", err)
	}
}

// A roster usually lives in the state tree, and a password there is a
// password in whatever distributes the tree.
func TestAPasswordInTheRosterIsWarnedAbout(t *testing.T) {
	r, err := ParseFlat([]byte("web1:\n  passwd: hunter2\n"), "roster")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Warnings) == 0 {
		t.Fatal("a password in the roster produced no warning")
	}
	if !strings.Contains(strings.Join(r.Warnings, " "), "password") {
		t.Errorf("the warning is %v", r.Warnings)
	}
}

// An empty roster is a roster with no targets, not a parse failure: an
// estate that has not filled one in should get "no targets matched".
func TestAnEmptyRosterIsNotAnError(t *testing.T) {
	r, err := ParseFlat([]byte("\n# nothing yet\n"), "roster")
	if err != nil {
		t.Fatalf("an empty roster failed: %v", err)
	}
	if len(r.Targets) != 0 {
		t.Errorf("it found %d targets", len(r.Targets))
	}
}

// `Host *` is settings for every host, not a machine to connect to.
func TestSSHConfigSkipsWildcardPatterns(t *testing.T) {
	src := `
Host *
    ServerAliveInterval 30

Host web1
    HostName 10.0.0.4
    User deploy
    Port 2222

Host bastion jump
    HostName bastion.example

Match host db*
    User dba
`
	r, err := ParseSSHConfig([]byte(src), "config")
	if err != nil {
		t.Fatal(err)
	}
	ids := strings.Join(r.IDs(), ",")
	if ids != "bastion,jump,web1" {
		t.Fatalf("it found %s", ids)
	}
	web, _ := r.Get("web1")
	if web.Host != "10.0.0.4" || web.User != "deploy" || web.Port != 2222 {
		t.Errorf("web1 read as %+v", web)
	}
}

// `Key=value` and `Key value` are both ssh config syntax.
func TestSSHConfigReadsBothOptionForms(t *testing.T) {
	r, err := ParseSSHConfig([]byte("Host web1\n  HostName=10.0.0.4\n  User deploy\n"), "config")
	if err != nil {
		t.Fatal(err)
	}
	web, _ := r.Get("web1")
	if web.Host != "10.0.0.4" || web.User != "deploy" {
		t.Errorf("it read %+v", web)
	}
}

// `[group:vars]` is not a list of hosts, and reading it as one produces
// targets called `ansible_user=deploy`.
func TestAnAnsibleINIInventorySkipsVarSections(t *testing.T) {
	src := `
[web]
web1.example ansible_host=10.0.0.4 ansible_user=deploy ansible_port=2222
web2.example

[web:vars]
ansible_become=true

[db]
db1.example ansible_become=true ansible_become_user=postgres
`
	r, err := parseAnsibleINI([]byte(src), "inventory")
	if err != nil {
		t.Fatal(err)
	}
	ids := strings.Join(r.IDs(), ",")
	if ids != "db1.example,web1.example,web2.example" {
		t.Fatalf("it found %s", ids)
	}
	web, _ := r.Get("web1.example")
	if web.Host != "10.0.0.4" || web.User != "deploy" || web.Port != 2222 {
		t.Errorf("web1 read as %+v", web)
	}
	db, _ := r.Get("db1.example")
	if !db.Sudo || db.SudoUser != "postgres" {
		t.Errorf("db1 read as %+v", db)
	}
}

// An inventory nests groups under `children`, and a reader that only
// looked at the top would find nothing on an estate that nests.
func TestAnAnsibleYAMLInventoryFindsNestedHosts(t *testing.T) {
	src := `
all:
  children:
    production:
      children:
        web:
          hosts:
            web1.example:
              ansible_host: 10.0.0.4
            web2.example: {}
      vars:
        ansible_user: deploy
`
	r, err := parseAnsibleYAML([]byte(src), "inventory.yml")
	if err != nil {
		t.Fatal(err)
	}
	ids := strings.Join(r.IDs(), ",")
	if ids != "web1.example,web2.example" {
		t.Fatalf("it found %s", ids)
	}
	web, _ := r.Get("web1.example")
	if web.Host != "10.0.0.4" {
		t.Errorf("web1 read as %+v", web)
	}
}

// A backend SPEC 21.2 names and this build does not read is refused by
// name, not reported as a typo.
func TestAnUnbuiltBackendIsNamedAsUnbuilt(t *testing.T) {
	if err := CheckBackend("flat"); err != nil {
		t.Errorf("flat was refused: %v", err)
	}
	for _, name := range []string{"scan", "cloud", "terraform"} {
		err := CheckBackend(name)
		if err == nil {
			t.Errorf("%s was accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "not built") {
			t.Errorf("%s: %v", name, err)
		}
	}
	err := CheckBackend("flatt")
	if err == nil || !strings.Contains(err.Error(), "is not a roster backend") {
		t.Errorf("a typo gave %v", err)
	}
}
