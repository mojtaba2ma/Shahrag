package health

// Reading what the machine actually is.
//
// The advanced nginx settings can only be filled in sensibly once the real
// core count, the real amount of memory, and the real descriptor limits are
// known. Guessing produces the two failure modes that make tuning advice
// worthless: numbers too small to help, or numbers so large that nginx
// refuses to start.
//
// Everything here reads /proc and /sys, which costs microseconds. The one
// genuinely expensive measurement — link speed — is handled separately and
// explicitly, because measuring bandwidth means sending traffic and that is
// never something to do behind the operator's back.

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	"shahrag/internal/config"
)

// ReadSysInfo gathers everything a recommendation is computed from.
//
// Every field degrades to zero rather than to a guess. config.Recommend
// treats zero as "unknown" and falls back to a conservative default, which
// is the right behaviour: a wrong number presented confidently is worse than
// an admitted gap.
func ReadSysInfo() config.SysInfo {
	si := config.SysInfo{Cores: countCores()}

	m := readMeminfo()
	si.RAMBytes = m["MemTotal"] * 1024
	si.AvailableBytes = m["MemAvailable"] * 1024
	si.SwapBytes = m["SwapTotal"] * 1024

	si.FileMax = readIntFile("/proc/sys/fs/file-max")
	si.NofileHard = readNofileHard()
	si.LinkMbits = readLinkSpeed()
	return si
}

// countCores counts what the SCHEDULER will actually give nginx, not what
// the hardware has.
//
// runtime.NumCPU respects GOMAXPROCS and the process's CPU affinity, which
// is what we want: on a container limited to one core, recommending
// "worker_processes auto" against eight physical cores would create eight
// workers fighting over one.
func countCores() int {
	// sched_getaffinity is the honest answer and is what runtime.NumCPU
	// uses on Linux.
	var set unix_CPUSet
	if n := set.count(); n > 0 {
		return n
	}
	return 1
}

// readNofileHard returns the hard RLIMIT_NOFILE this process could raise
// itself to.
//
// This is the ceiling on worker_rlimit_nofile. Asking nginx for more than
// the hard limit makes it fail to start with "setrlimit() failed", and that
// failure happens at BOOT, which is the worst possible time to discover a
// tuning mistake.
func readNofileHard() int {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		return 0
	}
	if rl.Max > 1<<31 {
		return 1 << 31
	}
	return int(rl.Max)
}

// readLinkSpeed reports the primary interface's negotiated speed in Mbit/s.
//
// Read from /sys/class/net/<iface>/speed, which the kernel fills in from the
// driver. It costs nothing and sends no traffic.
//
// It is often WRONG in the ways that matter here, and the caller has to know
// that: virtio interfaces on a VPS commonly report -1 or a nominal 10000
// that has nothing to do with the actual allocation, and a 100 Mbit VPS
// behind a 10 Gbit host NIC reports the host's figure. So this is used only
// as a hint, never as the basis for a number an operator would notice being
// wrong, and the panel offers a real measurement as a separate, explicit
// action.
func readLinkSpeed() int {
	iface := primaryInterface()
	if iface == "" {
		return 0
	}
	v := readIntFile("/sys/class/net/" + iface + "/speed")
	// -1 means "the driver does not know", which is the common case on a
	// virtual NIC. Anything absurd is also treated as unknown.
	if v <= 0 || v > 400000 {
		return 0
	}
	return v
}

// primaryInterface returns the interface carrying the default route.
//
// Parsed from /proc/net/route rather than shelled out to `ip route`,
// because this can be called during installation where PATH is minimal.
func primaryInterface() string {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		// Destination 00000000 is the default route.
		if f[1] == "00000000" {
			return f[0]
		}
	}
	return ""
}

func readIntFile(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return n
}
