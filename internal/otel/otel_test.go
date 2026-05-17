package otel_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"

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

func TestRecordError_FlipsSpanStatusAndAddsEvent(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	shutdown, err := figOtel.Init(ctx, dir)
	require.NoError(t, err)

	ctx, span := figOtel.Start(ctx, "test.failing")
	figOtel.RecordError(ctx, "test.failure", errors.New("boom"),
		attribute.String("detail", "context"),
	)
	span.End()

	require.NoError(t, shutdown(ctx))

	data, err := os.ReadFile(filepath.Join(dir, "traces.jsonl"))
	require.NoError(t, err)

	var spanData map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &spanData))

	status, ok := spanData["Status"].(map[string]interface{})
	require.True(t, ok, "span should have Status object")
	assert.Equal(t, "Error", status["Code"], "status code should flip to Error")
	assert.Equal(t, "test.failure", status["Description"])

	events, ok := spanData["Events"].([]interface{})
	require.True(t, ok, "span should have Events array")
	var sawNamed, sawException bool
	for _, e := range events {
		m := e.(map[string]interface{})
		switch m["Name"] {
		case "test.failure":
			sawNamed = true
		case "exception":
			sawException = true
		}
	}
	assert.True(t, sawNamed, "named event should be recorded")
	assert.True(t, sawException, "RecordError should also emit the standard exception event")
}

func TestRecordError_NoOpWithoutActiveSpan(t *testing.T) {
	// Plain background ctx has no recording span; must not panic.
	figOtel.RecordError(context.Background(), "nope", errors.New("x"))
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
