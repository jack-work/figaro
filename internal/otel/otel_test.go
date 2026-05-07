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

func TestInit_CreatesStateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")

	ctx := context.Background()
	shutdown, err := figOtel.Init(ctx, dir)
	require.NoError(t, err)
	defer shutdown(ctx)

	for _, name := range []string{"traces.jsonl", "logs.jsonl", "metrics.jsonl"} {
		_, err := os.Stat(filepath.Join(dir, name))
		assert.NoError(t, err, "%s should be created", name)
	}
}

func TestInit_RecordsSpans(t *testing.T) {
	dir := t.TempDir()

	ctx := context.Background()
	shutdown, err := figOtel.Init(ctx, dir)
	require.NoError(t, err)

	ctx, span := figOtel.Start(ctx, "test.operation")
	span.End()

	require.NoError(t, shutdown(ctx))

	data, err := os.ReadFile(filepath.Join(dir, "traces.jsonl"))
	require.NoError(t, err)
	assert.NotEmpty(t, data, "trace file should contain span data")

	var spanData map[string]interface{}
	err = json.Unmarshal(data, &spanData)
	require.NoError(t, err, "trace output should be valid JSON")
	assert.Equal(t, "test.operation", spanData["Name"], "span name should match")
}

func TestTracer_ReturnsFigaroTracer(t *testing.T) {
	dir := t.TempDir()

	ctx := context.Background()
	shutdown, err := figOtel.Init(ctx, dir)
	require.NoError(t, err)
	defer shutdown(ctx)

	tracer := figOtel.Tracer()
	assert.NotNil(t, tracer)
}

func TestStart_PropagatesContext(t *testing.T) {
	dir := t.TempDir()

	ctx := context.Background()
	shutdown, err := figOtel.Init(ctx, dir)
	require.NoError(t, err)

	ctx, parent := figOtel.Start(ctx, "parent")
	_, child := figOtel.Start(ctx, "child")

	parentTraceID := parent.SpanContext().TraceID()
	childTraceID := child.SpanContext().TraceID()

	assert.True(t, parentTraceID.IsValid(), "parent trace ID should be valid")
	assert.Equal(t, parentTraceID, childTraceID, "child should share parent's trace ID")

	child.End()
	parent.End()
	require.NoError(t, shutdown(ctx))
}
