package otel_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	figOtel "github.com/jack-work/figaro/internal/otel"
)

func TestInit_CreatesTraceFile(t *testing.T) {
	dir := t.TempDir()
	traceFile := filepath.Join(dir, "sub", "traces.jsonl")

	ctx := context.Background()
	shutdown, err := figOtel.Init(ctx, traceFile)
	require.NoError(t, err)
	defer shutdown(ctx)

	_, err = os.Stat(traceFile)
	assert.NoError(t, err, "trace file should be created including parent dirs")
}

func TestInit_RecordsSpans(t *testing.T) {
	dir := t.TempDir()
	traceFile := filepath.Join(dir, "traces.jsonl")

	ctx := context.Background()
	shutdown, err := figOtel.Init(ctx, traceFile)
	require.NoError(t, err)

	// Create a span.
	ctx, span := figOtel.Start(ctx, "test.operation")
	span.End()

	// Flush — shutdown writes pending spans.
	require.NoError(t, shutdown(ctx))

	// Read the trace file and verify a span was written.
	data, err := os.ReadFile(traceFile)
	require.NoError(t, err)
	assert.NotEmpty(t, data, "trace file should contain span data")

	// The stdout exporter writes one JSON object per span.
	var spanData map[string]interface{}
	err = json.Unmarshal(data, &spanData)
	require.NoError(t, err, "trace output should be valid JSON")
	assert.Equal(t, "test.operation", spanData["Name"], "span name should match")
}

func TestTracer_ReturnsFigaroTracer(t *testing.T) {
	dir := t.TempDir()
	traceFile := filepath.Join(dir, "traces.jsonl")

	ctx := context.Background()
	shutdown, err := figOtel.Init(ctx, traceFile)
	require.NoError(t, err)
	defer shutdown(ctx)

	tracer := figOtel.Tracer()
	assert.NotNil(t, tracer)
}

func TestStart_PropagatesContext(t *testing.T) {
	dir := t.TempDir()
	traceFile := filepath.Join(dir, "traces.jsonl")

	ctx := context.Background()
	shutdown, err := figOtel.Init(ctx, traceFile)
	require.NoError(t, err)

	// Parent span.
	ctx, parent := figOtel.Start(ctx, "parent")

	// Child span — should inherit trace ID from parent.
	_, child := figOtel.Start(ctx, "child")

	parentTraceID := parent.SpanContext().TraceID()
	childTraceID := child.SpanContext().TraceID()

	assert.True(t, parentTraceID.IsValid(), "parent trace ID should be valid")
	assert.Equal(t, parentTraceID, childTraceID, "child should share parent's trace ID")

	child.End()
	parent.End()
	require.NoError(t, shutdown(ctx))
}
