// Package winjob is the part of putting a Windows process in a job
// object that os/exec does not do: holding the process still until it
// is in the job.
//
// A job object can only be given a process that exists, and os/exec
// starts the process running. Assigning it afterwards leaves a gap in
// which the process can start a child of its own, and that child is not
// in the job: a timeout's TerminateJobObject does not reach it, and
// neither does JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE (issue 608 item 1).
// So the process is created suspended (CREATE_SUSPENDED, which
// SysProcAttr.CreationFlags passes through to CreateProcess), assigned,
// and then resumed here.
//
// It is used by internal/exec, for commands, and by internal/bridge, for
// extensions. The two differ in what they do when the assignment fails,
// and agree that a process which could not be resumed is killed rather
// than left suspended.
package winjob
