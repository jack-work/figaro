//go:build !race

package cli

// raceEnabled scales wall-clock bounds in stress tests: the race detector
// slows execution 5-15x, which is overhead, not a regression.
const raceEnabled = false
