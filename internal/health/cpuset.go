package health

import "runtime"

// unix_CPUSet wraps the core count behind a tiny type so sysinfo.go reads
// the same whether or not a future build wants to call sched_getaffinity
// directly.
//
// runtime.NumCPU already IS the affinity-aware answer on Linux: the runtime
// calls sched_getaffinity at startup and reports the number of CPUs this
// process may actually run on, which is exactly the figure that should drive
// worker_processes. Calling the syscall again by hand would duplicate the
// runtime's work and get the same number.
type unix_CPUSet struct{}

func (unix_CPUSet) count() int { return runtime.NumCPU() }
