// Package wintask is the Windows task scheduler, for the win_task
// module of SPEC sections 15.3 and 15.5.
//
// It goes through schtasks.exe with the scheduler's own XML, which is
// the opposite of the choice internal/winsvc made for services, and the
// difference is worth stating because the reasoning is not "one is
// easier".
//
// The service control manager was reached through its API because
// sc.exe writes a table for a person and its status words are
// localised: there is no machine-readable output mode, so parsing it
// would mean parsing prose. The task scheduler has one. `schtasks /query
// /xml` emits the Task Scheduler schema — a published XML format whose
// element names are fixed in every locale — and `schtasks /create /xml`
// takes the same format back. That is SPEC 15.2's stated standard for
// reaching a subsystem through its binary: "a machine-readable output
// mode".
//
// The alternative was the scheduler's COM API, which needs hand-rolled
// vtable dispatch across six interfaces and BSTR marshalling for a
// result the XML already gives exactly. The one thing the XML does not
// carry is whether a task is running right now, and schtasks reports
// that only as a localised word — so this package does not report it
// rather than parsing prose and getting it wrong on a German host.
package wintask
