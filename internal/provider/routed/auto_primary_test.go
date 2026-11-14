package routed

import (
	"context"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/models"
)

func TestAutoPrimaryAPIIsNotTheIngressUnionRanking(t *testing.T) {
	for _, endpoints := range [][]string{{"messages", "responses"}, {"responses", "messages"}, {"/v1/responses", "/v1/messages"}} {
		p := mustNew(t, Spec{Name: "auto-primary", BaseURL: func() string { return "http://127.0.0.1:1" }, Catalog: "/models", Default: Responses})
		p.cat = newCatalogCache(map[string]models.Entry{"fable-sol@auto": {Endpoints: endpoints, Auto: &models.AutoRoute{Generation: "g1", Candidates: []models.RouteCandidate{{Model: "vendor/fable", APIs: []string{"messages"}}, {Model: "vendor/sol", APIs: []string{"responses"}}}}}})
		p.cat.fetched = time.Now()
		got, err := p.route(context.Background(), "fable-sol@auto")
		if err != nil || got != Messages {
			t.Fatalf("%v route=%s err=%v", endpoints, got, err)
		}
		if p.cachedRoute("fable-sol@auto") != Messages {
			t.Fatal("cached route ignored primary")
		}
	}
}

func TestAutoPrimaryRequiresAnAdvertisedIngress(t *testing.T) {
	p := mustNew(t, Spec{Name: "auto-no-ingress", BaseURL: func() string { return "http://127.0.0.1:1" }, Catalog: "/models", Default: Responses})
	p.cat = newCatalogCache(map[string]models.Entry{"auto": {Endpoints: []string{"responses"}, Auto: &models.AutoRoute{Candidates: []models.RouteCandidate{{Model: "vendor/fable", APIs: []string{"messages"}}}}}})
	p.cat.fetched = time.Now()
	if _, err := p.route(context.Background(), "auto"); err == nil {
		t.Fatal("unsupported primary was routed")
	}
	if p.cachedRoute("auto") != "" {
		t.Fatal("cached route guessed default")
	}
}
