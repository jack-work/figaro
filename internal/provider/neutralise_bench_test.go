package provider

import (
	"strings"
	"testing"
)

func benchNeutralise(b *testing.B, out string) {
	b.SetBytes(int64(len(out)))
	b.ReportAllocs()
	for b.Loop() {
		_ = NeutraliseReservedTags(out)
	}
}

func BenchmarkNeutraliseOnCode(b *testing.B) {
	benchNeutralise(b, strings.Repeat("\tif a < b && len(xs) <= n { return <-ch }\n", 1200))
}

func BenchmarkNeutraliseWithoutAngle(b *testing.B) {
	benchNeutralise(b, strings.Repeat("plain log line with no angle bracket at all\n", 1200))
}

func BenchmarkNeutraliseOnRendererSource(b *testing.B) {
	benchNeutralise(b, strings.Repeat("\treturn \"<system-reminder name=\\\"\" + name + \"\\\">\" // a < b\n", 1000))
}

func BenchmarkTheRegexpItReplacedOnCode(b *testing.B) {
	out := strings.Repeat("\tif a < b && len(xs) <= n { return <-ch }\n", 1200)
	b.SetBytes(int64(len(out)))
	for b.Loop() {
		_ = neutralisedByRegexp(out)
	}
}
