//go:build windows

package winjob

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Resume lets a process created with CREATE_SUSPENDED run.
//
// CreateProcess hands back the handle of the primary thread, and that
// is what ResumeThread wants, but syscall.StartProcess closes it before
// returning and os/exec keeps only the process. So the threads are found
// again, with a Toolhelp snapshot, and every thread the process owns is
// resumed. A process that has never run has one: the loader's worker
// threads are started by the primary thread, which has not run yet.
//
// Two other ways were considered and not taken. NtResumeProcess does
// this in one call, but it is an undocumented ntdll export with no
// contract behind it, reached through a lazily loaded procedure, and if
// it ever changes the failure is a node whose every command hangs until
// its timeout. PROC_THREAD_ATTRIBUTE_JOB_LIST would put the process in
// the job as CreateProcess makes it, with no suspension at all, but
// syscall.SysProcAttr builds its own attribute list and offers no way to
// add an entry to it.
//
// The snapshot lists every thread on the machine and the process ID is
// what selects ours. That is sound because the caller holds a handle to
// the process (os.Process does), and while a handle is open the ID
// cannot be given to another process. ResumeThread on a thread that is
// not suspended does nothing, so a thread something else put into the
// process (a debugger, an endpoint agent) is not harmed. One that
// something else deliberately suspended would be resumed; that is the
// one case this is wrong about.
//
// Finding no thread is an error, not success: the process would stay
// suspended for ever, and a caller told it was running would wait on it
// until its timeout. The caller kills it.
func Resume(pid uint32) error {
	threads, err := Threads(pid)
	if err != nil {
		return fmt.Errorf("resuming process %d: %w", pid, err)
	}
	if err := resumeThreads(threads); err != nil {
		return fmt.Errorf("resuming process %d: %w", pid, err)
	}
	return nil
}

// resumeThreads resumes each listed thread, and fails unless at least
// one of them was resumed.
//
// A thread that has gone between the snapshot and OpenThread is skipped.
// OpenThread says ERROR_INVALID_PARAMETER for an ID that names no
// thread, and a thread that has exited names none. A process held
// suspended cannot lose a thread that way, but one that was running
// can, and it did: on a windows-2022 runner, with CREATE_SUSPENDED
// taken out on purpose, every command whose program had already started
// a short-lived thread failed with "opening thread 1872: The parameter
// is incorrect" and was killed (DIVERGENCE 5.620.2). Being strict there
// buys nothing: a thread that no longer exists is not one this was
// going to hold back.
func resumeThreads(threads []uint32) error {
	resumed := 0
	for _, tid := range threads {
		gone, err := resumeThread(tid)
		if err != nil {
			return err
		}
		if !gone {
			resumed++
		}
	}
	if resumed == 0 {
		return fmt.Errorf("it has no threads to resume (%d listed, none still there)", len(threads))
	}
	return nil
}

// resumeThread resumes one thread, and reports gone when it no longer
// exists.
func resumeThread(tid uint32) (gone bool, err error) {
	h, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, tid)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("opening thread %d: %w", tid, err)
	}
	defer windows.CloseHandle(h)
	// ResumeThread returns the previous suspend count, or 0xFFFFFFFF on
	// failure, which x/sys turns into err.
	if _, err := windows.ResumeThread(h); err != nil {
		return false, fmt.Errorf("resuming thread %d: %w", tid, err)
	}
	return false, nil
}

// Threads lists the IDs of the threads a process owns, from a Toolhelp
// snapshot taken now.
func Threads(pid uint32) ([]uint32, error) {
	// The process ID argument is ignored for TH32CS_SNAPTHREAD: the
	// snapshot is of every thread on the machine, and the filter is ours.
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return nil, fmt.Errorf("taking a thread snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)

	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	var threads []uint32
	for err = windows.Thread32First(snap, &entry); err == nil; err = windows.Thread32Next(snap, &entry) {
		if entry.OwnerProcessID == pid {
			threads = append(threads, entry.ThreadID)
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, fmt.Errorf("reading the thread snapshot: %w", err)
	}
	return threads, nil
}

// ProcessIDs lists the processes a job holds now. It is how a test sees
// what a job holds rather than what it was meant to.
func ProcessIDs(job windows.Handle) ([]uint32, error) {
	// JOBOBJECT_BASIC_PROCESS_ID_LIST, which x/sys does not define: two
	// counts, then the IDs as ULONG_PTRs. A list that does not fit is
	// ERROR_MORE_DATA, not a silent truncation.
	const capacity = 1024
	type idList struct {
		Assigned uint32
		Listed   uint32
		IDs      [capacity]uintptr
	}
	var list idList
	if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicProcessIdList,
		uintptr(unsafe.Pointer(&list)), uint32(unsafe.Sizeof(list)), nil); err != nil {
		return nil, fmt.Errorf("listing the job's processes: %w", err)
	}
	ids := make([]uint32, 0, list.Listed)
	for _, id := range list.IDs[:list.Listed] {
		ids = append(ids, uint32(id))
	}
	return ids, nil
}
