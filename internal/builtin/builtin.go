// Package builtin holds the execution and state modules compiled into
// halite: the Core tier of SPEC section 15.
//
// Both registries are built here rather than in separate packages because
// a state module and its execution module are two halves of one feature,
// and splitting them across packages makes it easy for the pair to drift.
package builtin

import (
	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
)

// Registries is the pair a node runs with.
type Registries struct {
	Exec   *exec.Registry
	States *states.Registry
}

// New builds the registries for this platform.
func New() *Registries {
	r := &Registries{
		Exec:   exec.NewRegistry(),
		States: states.NewRegistry(),
	}
	registerTest(r)
	registerCmd(r)
	registerFile(r)
	registerFileEdit(r)
	registerUser(r)
	registerHosts(r)
	registerSysctl(r)
	registerSysrc(r)
	registerCron(r)
	registerPkg(r)
	registerService(r)
	registerIntrospect(r)
	registerCrypto(r)
	registerDataStore(r)
	registerArchive(r)
	registerSystem(r)
	registerSSH(r)
	registerGit(r)

	// The language and runtime modules of SPEC section 15.4, and the
	// three states section 15.5 names for them.
	registerPip(r)
	registerVirtualenv(r)
	registerNpm(r)
	registerGem(r)
	registerCargo(r)
	registerGoTool(r)
	registerComposer(r)
	registerCpan(r)
	registerMaven(r)
	registerLangStates(r)

	// x509, SPEC sections 15.2 and 15.5.
	registerX509(r)
	registerX509States(r)
	registerGrainsStates(r)
	registerPkgVersion(r)
	registerFileMore(r)
	registerServiceMore(r)
	registerCmdMore(r)
	registerCmdScriptState(r)
	registerPkgMore(r)
	registerZFS(r)
	registerSys(r)
	registerBeacons(r)
	registerSchedule(r)
	registerMine(r)
	return r
}

// ---- signature shorthands ----
//
// A module's signature is declared next to its implementation, and these
// keep the declaration readable without hiding what it says.

func req(name string, t signature.Type, doc string) signature.Param {
	return signature.Param{Name: name, Type: t, Required: true, Doc: doc}
}

func opt(name string, t signature.Type, def any, doc string) signature.Param {
	return signature.Param{Name: name, Type: t, Default: def, Doc: doc}
}

func choice(name string, def any, doc string, choices ...string) signature.Param {
	return signature.Param{Name: name, Type: signature.String, Default: def, Doc: doc, Choices: choices}
}

// nameParam is the parameter almost every state takes, defaulting to the
// state ID.
func nameParam(doc string) signature.Param {
	return signature.Param{Name: "name", Type: signature.String, Doc: doc}
}

// pathParam is nameParam for a state that acts on a filesystem path.
func pathParam(doc string) signature.Param {
	return signature.Param{Name: "name", Type: signature.Path, Doc: doc}
}
