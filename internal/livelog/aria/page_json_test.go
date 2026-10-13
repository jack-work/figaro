package aria

import (
	"encoding/json"
	"strings"
	"testing"
)

// An empty page must still carry a parts key.
func TestEmptyPageCarriesAnEmptyPartsArray(t *testing.T) {
	b, err := json.Marshal(Page{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"parts":[]`) {
		t.Fatalf("empty page = %s, want a parts array", b)
	}
	var back struct {
		Parts []TurnPart `json:"parts"`
	}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Parts == nil {
		t.Fatal("parts decoded as nil")
	}
}

func TestPageWithPartsStillCarriesThem(t *testing.T) {
	b, err := json.Marshal(Page{Parts: []TurnPart{{From: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"from":3`) {
		t.Fatalf("page = %s, want the part", b)
	}
}
