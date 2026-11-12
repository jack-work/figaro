package daemon

import (
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/jack-work/figaro/internal/boot"
)

func bootReason(getenv func(string) string) string {
	switch {
	case getenv(boot.BootReasonEnv) != "":
		return getenv(boot.BootReasonEnv)
	case getenv("_FIGARO_DAEMON") == "1":
		return "cli"
	case getenv("INVOCATION_ID") != "":
		return "unit"
	}
	return "direct"
}

func takeStopStamp(runtimeDir string, now time.Time) (time.Duration, bool) {
	path := boot.StopStampPath(runtimeDir)
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	os.Remove(path)
	ms, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0, false
	}
	return now.Sub(time.UnixMilli(ms)), true
}

func bootAttrs(getenv func(string) string, runtimeDir string, start time.Time, arias int) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("boot.reason", bootReason(getenv)),
		attribute.Int("store.arias", arias),
	}
	if gap, ok := takeStopStamp(runtimeDir, start); ok {
		attrs = append(attrs, attribute.Int64("boot.after_stop_ms", gap.Milliseconds()))
	}
	return attrs
}
