package engine

import (
	"log"

	"golang.org/x/sys/unix"
)

// Code, bash and task nodes run pipeline authors' programs as children of
// this process, under the same user. Their own environment is filtered
// (codeexec.WorkerEnv), but /proc/<this pid>/environ is not: it holds the
// whole environment this process started with -- database URL, signing
// and encryption keys -- and a child of the same user, or root in a
// container, could read it, and could read this process's memory the same
// way.
//
// A non-dumpable process's /proc files belong to root and need
// CAP_SYS_PTRACE to read, which neither an unprivileged user nor a
// container's root holds by default. Set here, in the package every binary
// that runs pipeline code imports, so none of them can forget it. Children
// are dumpable again after exec, which is fine: theirs is the filtered
// environment.
func init() {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		log.Printf("[engine] could not mark the process non-dumpable: %v", err)
	}
}
