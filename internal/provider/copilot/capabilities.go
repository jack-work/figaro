package copilot

import (
	"fmt"
	"strings"
)

// Model-shape rules published by OpenAI, checked before a request is built:
// developers.openai.com/api/docs/models/gpt-6-astra and
// .../guides/latest-model. GPT-6 Astra rejects temperature, top_p and the
// "none" reasoning effort with HTTP 400, and the migration guide sends
// "none"/"minimal" users to "low".
//
// Only models with published rules are constrained. An unknown id, including a
// model released after this code, passes through with whatever the form asked
// for: guessing at a future model's accepted shape breaks it on the day it
// ships, and the server's own 400 is a better teacher than our stale table.
type modelCapabilities struct {
	family string
	// reasoningEfforts is the accepted set; nil means unconstrained.
	reasoningEfforts []string
	// effortAdvice maps a rejected effort to the substitution the docs name.
	effortAdvice map[string]string
	// rejectsSampling covers temperature and top_p together, as the docs do.
	rejectsSampling bool
}

var astraCapabilities = modelCapabilities{
	family:           "gpt-6-astra",
	reasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
	effortAdvice: map[string]string{
		"none":    "low",
		"minimal": "low",
	},
	rejectsSampling: true,
}

var knownCapabilities = []modelCapabilities{astraCapabilities}

// verbosityLevels is the Responses API enum for text.verbosity, which is
// model-independent: "low", "medium", "high".
var verbosityLevels = []string{"low", "medium", "high"}

func capabilitiesFor(model string) (modelCapabilities, bool) {
	name := strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		name = name[slash+1:]
	}
	for _, caps := range knownCapabilities {
		// Prefix, so dated snapshots such as gpt-6-astra-2026-08-01 carry
		// the family's rules.
		if name == caps.family || strings.HasPrefix(name, caps.family+"-") {
			return caps, true
		}
	}
	return modelCapabilities{}, false
}

// validateCapabilities refuses a request the model is documented to reject,
// naming the form key and the fix. Nothing is dropped silently: a setting the
// user wrote either goes on the wire or produces an error.
func validateCapabilities(model string, options responseRequestOptions) error {
	var problems []string

	if options.text != nil && options.text.Verbosity != "" {
		if !contains(verbosityLevels, options.text.Verbosity) {
			problems = append(problems, fmt.Sprintf(
				"system.verbosity %q is not a Responses verbosity level (%s)",
				options.text.Verbosity, quoteList(verbosityLevels)))
		}
	}

	caps, known := capabilitiesFor(model)
	if !known {
		return joinProblems(model, problems)
	}

	if caps.rejectsSampling {
		if options.temperature != nil {
			problems = append(problems, fmt.Sprintf(
				"%s does not accept system.temperature; unset it", caps.family))
		}
		if options.topP != nil {
			problems = append(problems, fmt.Sprintf(
				"%s does not accept system.top_p; unset it", caps.family))
		}
	}

	if caps.reasoningEfforts != nil && options.reasoning != nil && options.reasoning.Effort != "" {
		effort := options.reasoning.Effort
		if !contains(caps.reasoningEfforts, effort) {
			problem := fmt.Sprintf(
				"%s does not support system.thinking_effort %q; supported: %s",
				caps.family, effort, quoteList(caps.reasoningEfforts))
			if advice, ok := caps.effortAdvice[strings.ToLower(effort)]; ok {
				problem += fmt.Sprintf(" (start with %q)", advice)
			}
			problems = append(problems, problem)
		}
	}

	return joinProblems(model, problems)
}

func joinProblems(model string, problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("copilot responses: %s: %s", model, strings.Join(problems, "; "))
}

func contains(set []string, value string) bool {
	for _, item := range set {
		if item == value {
			return true
		}
	}
	return false
}

func quoteList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, fmt.Sprintf("%q", value))
	}
	return strings.Join(quoted, ", ")
}
