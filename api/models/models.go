package models

import (
	"encoding/json"
	"time"

	"github.com/jack-work/figaro/api/form"
)

const FormID = "@models"

const IRVersion = 1

const (
	SourceAPI     = "api"
	SourceMapping = "mapping"
	SourceConfig  = "config"
)

const (
	ProviderKey     = "system.provider"
	ModelKey        = "system.model"
	OverrideKey     = "system.max_context_tokens"
	ContextTierKey  = "system.context_tier"
	LongContextTier = "long_context"
)

type Context struct {
	Default int `json:"default,omitempty"`
	Long    int `json:"long,omitempty"`
}

type Entry struct {
	Name       string            `json:"name,omitempty"`
	Context    Context           `json:"context,omitzero"`
	MaxOutput  int               `json:"max_output,omitempty"`
	Images     *bool             `json:"images,omitempty"`
	ImageBytes int               `json:"max_prompt_image_size,omitempty"`
	ImageCount int               `json:"max_prompt_images,omitempty"`
	Tools      *bool             `json:"tools,omitempty"`
	Reasoning  *bool             `json:"reasoning,omitempty"`
	Hidden     bool              `json:"hidden,omitempty"`
	Endpoints  []string          `json:"endpoints,omitempty"`
	Src        map[string]string `json:"src,omitempty"`
}

func (e Entry) Window(tier string) int {
	if tier == LongContextTier && e.Context.Long > 0 {
		return e.Context.Long
	}
	return e.Context.Default
}

func (e Entry) TakesImages() (takes bool, src string) {
	if e.Images == nil {
		return false, ""
	}
	return *e.Images, e.Source("images")
}

func (e Entry) Source(field string) string { return e.Src[field] }

func (e *Entry) Mark(field, src string) {
	if e.Src == nil {
		e.Src = map[string]string{}
	}
	e.Src[field] = src
}

type Provider struct {
	IR          int              `json:"ir,omitempty"`
	Models      map[string]Entry `json:"models,omitempty"`
	RefreshedAt int64            `json:"refreshed_at,omitempty"`
	AttemptedAt int64            `json:"attempted_at,omitempty"`
	Error       string           `json:"error,omitempty"`
}

type Status struct {
	RefreshedAt time.Time
	AttemptedAt time.Time
	Error       string
}

func Providers(catalog form.Snapshot) (map[string]Provider, error) {
	raw, err := json.Marshal(catalog)
	if err != nil {
		return nil, err
	}
	out := map[string]Provider{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	for name, p := range out {
		if p.IR != IRVersion {
			p.Models = nil
			out[name] = p
		}
	}
	return out, nil
}

func Current(catalog form.Snapshot, provider string) bool {
	n, ok := intAt(catalog, provider+".ir")
	return ok && n == IRVersion
}

func Lookup(catalog form.Snapshot, provider, model string) (Entry, bool) {
	var e Entry
	if provider == "" || model == "" || !Current(catalog, provider) {
		return e, false
	}
	raw, ok := catalog.Get(provider + ".models." + model)
	if !ok || json.Unmarshal(raw, &e) != nil {
		return Entry{}, false
	}
	return e, true
}

func ProviderStatus(catalog form.Snapshot, provider string) (Status, bool) {
	if provider == "" {
		return Status{}, false
	}
	var st Status
	found := false
	if ms, ok := intAt(catalog, provider+".refreshed_at"); ok {
		st.RefreshedAt, found = time.UnixMilli(int64(ms)), true
	}
	if ms, ok := intAt(catalog, provider+".attempted_at"); ok {
		st.AttemptedAt, found = time.UnixMilli(int64(ms)), true
	}
	if raw, ok := catalog.Get(provider + ".error"); ok {
		_ = json.Unmarshal(raw, &st.Error)
		found = true
	}
	return st, found
}

func Override(bound form.Snapshot) (int, bool) {
	n, ok := intAt(bound, OverrideKey)
	if !ok || n <= 0 {
		return 0, false
	}
	return n, true
}

func Subject(bound form.Snapshot) (provider, model string) {
	return stringAt(bound, ProviderKey), stringAt(bound, ModelKey)
}

func ContextLimit(catalog, bound form.Snapshot) int {
	if n, ok := Override(bound); ok {
		return n
	}
	provider, model := Subject(bound)
	return EntryLimit(catalog, provider, model, stringAt(bound, ContextTierKey))
}

func EntryLimit(catalog form.Snapshot, provider, model, tier string) int {
	if provider == "" || model == "" || !Current(catalog, provider) {
		return 0
	}
	raw, ok := catalog.Get(provider + ".models." + model + ".context")
	var c Context
	if !ok || json.Unmarshal(raw, &c) != nil {
		return 0
	}
	return Entry{Context: c}.Window(tier)
}

func intAt(s form.Snapshot, key string) (int, bool) {
	raw, ok := s.Get(key)
	if !ok {
		return 0, false
	}
	var n int
	if json.Unmarshal(raw, &n) != nil {
		return 0, false
	}
	return n, true
}

func stringAt(s form.Snapshot, key string) string {
	raw, ok := s.Get(key)
	if !ok {
		return ""
	}
	var v string
	_ = json.Unmarshal(raw, &v)
	return v
}
