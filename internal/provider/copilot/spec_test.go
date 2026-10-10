package copilot

import (
	"testing"

	"github.com/jack-work/figaro/internal/provider/responses"
	"github.com/jack-work/figaro/internal/provider/routed"
)

func TestTheCopilotSpecCarriesItsHandshake(t *testing.T) {
	spec := Spec(newTokenSource(staticResolver("t"), Config{BaseURL: "http://x"}))
	if spec.Headers["Copilot-Integration-Id"] == "" || spec.Headers["X-GitHub-Api-Version"] != copilotAPIVersion {
		t.Errorf("static headers %v", spec.Headers)
	}
	if spec.TransportHeaders[routed.Messages]["anthropic-dangerous-direct-browser-access"] != "true" {
		t.Error("the messages-only header is missing")
	}
	ids := spec.IdentityHeaders(responses.IDs{Task: "task-1", Session: "session-1", Interaction: "i-1", Machine: "m-1"})
	for k, want := range map[string]string{
		"X-Agent-Task-Id": "task-1", "X-Client-Session-Id": "session-1",
		"X-Interaction-Id": "i-1", "X-Client-Machine-Id": "m-1", "X-Github-Repository-Host": "github.com",
	} {
		if ids[k] != want {
			t.Errorf("%s=%q, want %q", k, ids[k], want)
		}
	}
	if spec.Catalog != "/models" {
		t.Errorf("catalog %q", spec.Catalog)
	}
}
