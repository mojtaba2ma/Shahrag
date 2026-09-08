//go:build !race

package banner

// raceEnabled is false in an ordinary build.
//
// Timing budgets have to know: the race detector instruments every lock and
// multiplies contended timings by roughly ten, so a budget tight enough to
// be meaningful normally is guaranteed to fail under -race even when the
// code is correct.
const raceEnabled = false
