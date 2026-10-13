package responses

import (
	"fmt"
	"strings"

	"github.com/jack-work/figaro/api/form"
)

// Published model-shape rules, checked before a request is built. Only
// models with published rules are constrained; an unknown id passes through
// and the server's own 400 is the teacher.
type modelCapabilities struct {
	family string
	// nil means unconstrained.
	reasoningEfforts []string
	effortAdvice     map[string]string
	rejectsSampling  bool
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

var verbosityLevels = []string{"low", "medium", "high"}

func capabilitiesFor(model string) (modelCapabilities, bool) {
	name := strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		name = name[slash+1:]
	}
	for _, caps := range knownCapabilities {
		// Dated snapshots carry the family's rules.
		if name == caps.family || strings.HasPrefix(name, caps.family+"-") {
			return caps, true
		}
	}
	return modelCapabilities{}, false
}

// validateCapabilities refuses, naming the form key and the fix; a setting
// the user wrote either goes on the wire or errors.
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
	return fmt.Errorf("responses: %s: %s", model, strings.Join(problems, "; "))
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

func Prevalidate(model string, snapshot form.Snapshot) error {
	if _, known := capabilitiesFor(model); !known {
		return nil
	}
	options, err := responseOptionsFor(snapshot)
	if err != nil {
		return err
	}
	return validateCapabilities(model, options)
}
