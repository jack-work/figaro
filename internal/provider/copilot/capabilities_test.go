package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type refusingTokenSource struct{ resolves int }

func (s *refusingTokenSource) Resolve() (string, error) {
	s.resolves++
	return "", errors.New("credentials must not be resolved")
}

func (s *refusingTokenSource) Invalidate(string) error { return nil }

func float64Ptr(v float64) *float64 { return &v }

func TestCapabilitiesForRecognizesAstraSnapshots(t *testing.T) {
	for _, model := range []string{
		"gpt-6-astra",
		"GPT-6-Astra",
		"gpt-6-astra-2026-08-01",
		"copilot/gpt-6-astra",
		"  gpt-6-astra  ",
	} {
		caps, ok := capabilitiesFor(model)
		require.True(t, ok, model)
		assert.Equal(t, "gpt-6-astra", caps.family)
	}
}

func TestCapabilitiesLeaveOtherModelsUnconstrained(t *testing.T) {
	for _, model := range []string{
		"",
		"gpt-5.6-terra",
		"gpt-5.5",
		"claude-opus-9",
		"gpt-7-nebula",
		"gpt-6-astral",
	} {
		_, ok := capabilitiesFor(model)
		assert.False(t, ok, model)

		// A model we know nothing about keeps every knob the form set,
		// including ones Astra rejects.
		require.NoError(t, validateCapabilities(model, responseRequestOptions{
			temperature: float64Ptr(0.7),
			topP:        float64Ptr(0.9),
			reasoning:   &responseReasoning{Effort: "none"},
		}))
	}
}

func TestAstraRejectsSamplingControls(t *testing.T) {
	err := validateCapabilities("gpt-6-astra", responseRequestOptions{temperature: float64Ptr(0.2)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "system.temperature")
	assert.Contains(t, err.Error(), "gpt-6-astra")

	err = validateCapabilities("gpt-6-astra", responseRequestOptions{topP: float64Ptr(0.5)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "system.top_p")
}

func TestAstraRejectsUnsupportedReasoningEffort(t *testing.T) {
	err := validateCapabilities("gpt-6-astra", responseRequestOptions{
		reasoning: &responseReasoning{Effort: "none"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "system.thinking_effort \"none\"")
	assert.Contains(t, err.Error(), "start with \"low\"")

	err = validateCapabilities("gpt-6-astra", responseRequestOptions{
		reasoning: &responseReasoning{Effort: "minimal"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start with \"low\"")

	err = validateCapabilities("gpt-6-astra", responseRequestOptions{
		reasoning: &responseReasoning{Effort: "ludicrous"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "\"low\", \"medium\", \"high\", \"xhigh\", \"max\"")
}

func TestAstraAcceptsDocumentedReasoningEfforts(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		require.NoError(t, validateCapabilities("gpt-6-astra", responseRequestOptions{
			reasoning: &responseReasoning{Effort: effort, Summary: "auto"},
		}), effort)
	}
	require.NoError(t, validateCapabilities("gpt-6-astra", responseRequestOptions{
		reasoning: &responseReasoning{Context: "all_turns"},
		text:      &responseText{Verbosity: "high"},
	}))
}

func TestVerbosityIsValidatedForEveryModel(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-5.6-terra", "gpt-7-nebula"} {
		err := validateCapabilities(model, responseRequestOptions{
			text: &responseText{Verbosity: "chatty"},
		})
		require.Error(t, err, model)
		assert.Contains(t, err.Error(), "system.verbosity")

		for _, level := range verbosityLevels {
			require.NoError(t, validateCapabilities(model, responseRequestOptions{
				text: &responseText{Verbosity: level},
			}), level)
		}
	}
}

func TestCapabilityErrorNamesEveryIncompatibleSetting(t *testing.T) {
	err := validateCapabilities("gpt-6-astra", responseRequestOptions{
		temperature: float64Ptr(0.2),
		reasoning:   &responseReasoning{Effort: "none"},
		text:        &responseText{Verbosity: "verbose"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "system.verbosity")
	assert.Contains(t, err.Error(), "system.temperature")
	assert.Contains(t, err.Error(), "system.thinking_effort")
}

func TestSendRejectsAstraSamplingBeforeResolvingCredentials(t *testing.T) {
	tokenSource := &refusingTokenSource{}
	p := newResponsesProvider(provider.Knobs{Model: "gpt-6-astra"}, tokenSource, "",
		func(string) (store.Log[[]json.RawMessage], error) {
			return store.NewMemLog[[]json.RawMessage](), nil
		})
	p.baseURL = func(string) string { return "http://127.0.0.1:1" }
	p.dial = func(context.Context, string, http.Header) (*websocket.Conn, error) {
		t.Fatal("dialed the provider despite an incompatible request")
		return nil, nil
	}

	in := provider.SendInput{
		FigLog: newResponsesInputLog(t),
		Snapshot: form.FromMap(map[string]json.RawMessage{
			"system.model":           json.RawMessage(`"gpt-6-astra"`),
			"system.temperature":     json.RawMessage(`0.4`),
			"system.thinking_effort": json.RawMessage(`"none"`),
		}),
	}

	err := p.Send(context.Background(), in, &responseTestBus{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gpt-6-astra")
	assert.Zero(t, tokenSource.resolves, "credentials resolved before validation")
}

func TestCopilotRejectsInvalidAstraSettingsBeforeCatalogFetch(t *testing.T) {
	source := &refusingTokenSource{}
	p, err := New(provider.Knobs{Model: "gpt-6-astra"}, source, Config{}, nil, nil)
	require.NoError(t, err)
	err = p.Send(context.Background(), provider.SendInput{
		Snapshot: form.FromMap(map[string]json.RawMessage{"system.thinking_effort": json.RawMessage(`"none"`)}),
	}, &responseTestBus{})
	require.ErrorContains(t, err, "system.thinking_effort")
	require.Zero(t, source.resolves, "routing must not fetch a catalog before local validation")
}
