package crashtest

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"time"
)

func killDelay(rng *rand.Rand) time.Duration {
	switch r := rng.Intn(100); {
	case r < 40:
		return time.Duration(3+rng.Intn(97)) * time.Millisecond
	case r < 65:
		return time.Duration(100+rng.Intn(150)) * time.Millisecond
	case r < 85:
		return time.Duration(250+rng.Intn(250)) * time.Millisecond
	default:
		return time.Duration(700+rng.Intn(700)) * time.Millisecond
	}
}

func runChildOnce(dir string, childSeed int64, salt string, delay time.Duration, m *model) (int64, error) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(),
		"FIGWAL_CRASH_CHILD=1",
		"CRASH_DIR="+dir,
		fmt.Sprintf("CRASH_SEED=%d", childSeed),
		"CRASH_SALT="+salt,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	done := make(chan []string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var lines []string
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		done <- lines
	}()
	time.Sleep(delay)
	killedAt := time.Now()
	_ = cmd.Process.Kill()
	lines := <-done
	werr := cmd.Wait()
	for i, l := range lines {
		if aerr := m.apply(l); aerr != nil {
			// Only the final line can be torn by the kill.
			if i == len(lines)-1 {
				break
			}
			return 0, fmt.Errorf("%s: %v", vChild, aerr)
		}
	}
	killed := cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == -1
	if !killed && !(m.closedClean && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 0) {
		return 0, fmt.Errorf("%s: child exited on its own: %v\nstderr: %s", vChild, werr, stderr.String())
	}
	return durableCutoff(m, killedAt), nil
}

// durableCutoff is the append-stamp bound the contract guarantees durable:
// SyncInterval behind the last moment the flusher was known alive (kill
// time, or Close start if the child began shutting down), minus scheduling
// slack. A completed Close makes everything reported durable.
func durableCutoff(m *model, killedAt time.Time) int64 {
	if m.closedClean {
		return math.MaxInt64
	}
	lag := flushInterval + flushSlack
	cutoff := killedAt.Add(-lag).UnixNano()
	if cb := m.closeBegunStamp; cb > 0 && cb-int64(lag) < cutoff {
		cutoff = cb - int64(lag)
	}
	if cutoff < 0 {
		cutoff = 0
	}
	return cutoff
}
