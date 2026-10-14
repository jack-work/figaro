package otel_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"

	figOtel "github.com/jack-work/figaro/internal/otel"
)

type exportedMetric struct {
	Name string
	Data struct {
		DataPoints []struct{ Bounds []float64 }
	}
}

func exportedMetrics(t *testing.T, dir string) map[string]exportedMetric {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "metrics.jsonl"))
	require.NoError(t, err)
	out := map[string]exportedMetric{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, 1<<24)
	for sc.Scan() {
		var line struct {
			ScopeMetrics []struct{ Metrics []exportedMetric }
		}
		require.NoError(t, json.Unmarshal(sc.Bytes(), &line))
		for _, sm := range line.ScopeMetrics {
			for _, m := range sm.Metrics {
				out[m.Name] = m
			}
		}
	}
	return out
}

func TestInstruments_EveryRecorderLandsInTheFile(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	shutdown, err := figOtel.Init(ctx, dir)
	require.NoError(t, err)
	figOtel.SetMemProbe(func() figOtel.MemSample {
		return figOtel.MemSample{HeapInuse: 1, HeapReleased: 2, ResidentArias: 3, Goroutines: 4}
	})

	attrs := []attribute.KeyValue{attribute.String("provider", "test")}
	figOtel.RecordRequestDuration(ctx, time.Second, attrs...)
	figOtel.RecordTTFT(ctx, time.Second, attrs...)
	figOtel.RecordTurn(ctx, time.Second, 2, attrs...)
	figOtel.RecordTool(ctx, time.Millisecond, attrs...)
	figOtel.RecordCatchUp(ctx, time.Millisecond, 5, 1, attrs...)
	figOtel.AddTokens(ctx, 1, 2, 3, 4, attrs...)
	figOtel.RecordRPC(ctx, time.Millisecond, attrs...)
	figOtel.RecordStoreOpen(ctx, time.Millisecond)
	figOtel.RecordSweep(ctx, time.Millisecond, attribute.String("phase", "test"))
	figOtel.RecordEvicted(ctx, 7)
	figOtel.RecordRequestBytes(ctx, 1024)
	figOtel.RecordOutfitWalk(ctx, time.Millisecond, 40)
	figOtel.RecordOutfitDress(ctx, time.Millisecond)
	figOtel.RecordDecideUpstream(ctx, time.Second, attrs...)
	figOtel.RecordDecideWindow(ctx, 12000, attrs...)
	require.NoError(t, shutdown(ctx))

	got := exportedMetrics(t, dir)
	for _, name := range []string{
		"figaro.request.duration",
		"figaro.provider.ttft",
		"figaro.turn.duration",
		"figaro.turn.rounds",
		"figaro.tool.duration",
		"figaro.translator.catchup",
		"figaro.translator.catchup.visited",
		"figaro.translator.catchup.encoded",
		"figaro.tokens",
		"figaro.rpc.duration",
		"figaro.store.open",
		"figaro.sweep.duration",
		"figaro.sweep.evicted",
		"figaro.provider.request_bytes",
		"figaro.outfit.walk",
		"figaro.outfit.walk.files",
		"figaro.outfit.dress",
		"figaro.decide.upstream",
		"figaro.decide.window",
		"figaro.mem.rss",
		"figaro.mem.heap_inuse",
		"figaro.mem.heap_released",
		"figaro.mem.resident_arias",
		"figaro.mem.goroutines",
	} {
		assert.Contains(t, got, name)
	}
}

func TestRequestDuration_ExplicitBoundaries(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	shutdown, err := figOtel.Init(ctx, dir)
	require.NoError(t, err)
	figOtel.RecordRequestDuration(ctx, 3*time.Second)
	require.NoError(t, shutdown(ctx))

	m, ok := exportedMetrics(t, dir)["figaro.request.duration"]
	require.True(t, ok)
	require.Len(t, m.Data.DataPoints, 1)
	assert.Equal(t, []float64{250, 500, 1000, 2000, 4000, 8000, 16000, 32000, 64000, 128000, 256000},
		m.Data.DataPoints[0].Bounds)
}
