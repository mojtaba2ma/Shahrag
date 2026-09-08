package health

import "testing"

// allocsPerRun exists only so the budget test above reads clearly.
// testing.AllocsPerRun cannot be called from a helper in the same file
// without shadowing, so it lives here.
func allocsPerRun(runs int, f func()) float64 {
	return testing.AllocsPerRun(runs, f)
}
