package routed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"

	"github.com/jack-work/figaro/api/form"
	apimodels "github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
)

const PresetGateway = "gateway"

func RegisterDeclared(loaded *config.Loaded) []string {
	if loaded == nil {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(loaded.ProvidersDir(), "*.toml"))
	sort.Strings(paths)
	var names []string
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".toml")
		reg, err := declared(loaded, name)
		if err != nil {
			slog.Warn("provider file ignored", "path", path, "err", err)
			continue
		}
		if reg == nil {
			continue
		}
		provider.Register(reg)
		names = append(names, name)
	}
	return names
}

func declared(loaded *config.Loaded, name string) (*provider.Registration, error) {
	f, err := LoadFile(loaded, name)
	if err != nil {
		return nil, err
	}
	if f.Preset == "" && f.API != "" {
		f.Preset = PresetGateway
	}
	if f.Preset == "" {
		return nil, nil
	}
	if existing := provider.Lookup(name); existing != nil && !existing.Declared {
		return nil, fmt.Errorf("%q is a built-in provider; preset applies only to a new name", name)
	}
	if f.Preset != PresetGateway {
		return nil, fmt.Errorf("preset %q is not one of: %s", f.Preset, PresetGateway)
	}
	if f.OAuth != nil && f.ServiceToken != "" {
		return nil, fmt.Errorf("[oauth] and service_token both name the credential; keep one")
	}
	reg := &provider.Registration{
		Name:     name,
		EnvVar:   f.APIKeyEnv,
		Declared: true,
		HasOAuth: f.OAuth != nil,
		Build: func(ctx provider.BuildContext) (provider.Provider, error) {
			p, err := buildGatewayAs(ctx, name, name)
			if err != nil {
				return nil, err
			}
			rp, ok := p.(*Provider)
			if !ok || rp.spec.Effort.Empty() {
				return p, nil
			}
			return NewDeclared(rp)
		},
	}
	if f.OAuth != nil {
		reg.OAuth = f.OAuth
		reg.LoginHint = "OAuth gateway (providers/" + name + ".toml):  figaro provider login " + name
	}
	return reg, nil
}

type DeclaredProvider struct {
	*Provider
	mapRe     *regexp.Regexp
	modelIdx  int
	effortIdx int
	outTmpl   *template.Template
	shared    *effortCache
}

var _ provider.CacheKeeper = (*DeclaredProvider)(nil)

func NewDeclared(p *Provider) (*DeclaredProvider, error) {
	d := &DeclaredProvider{
		Provider:  p,
		modelIdx:  -1,
		effortIdx: -1,
		shared:    sharedEffortCache(p.spec.Name, p.base(), p.spec.Effort.MapModel),
	}
	if p.spec.Effort.MapModel != "" {
		re, err := regexp.Compile(p.spec.Effort.MapModel)
		if err != nil {
			return nil, fmt.Errorf("%s: effort.map_model: %w", p.spec.Name, err)
		}
		d.mapRe = re
		d.modelIdx = re.SubexpIndex("model")
		d.effortIdx = re.SubexpIndex("effort")
	}
	if p.spec.Effort.OutboundModelTemplate != "" {
		tmpl, err := template.New("outbound_model").Option("missingkey=error").Parse(p.spec.Effort.OutboundModelTemplate)
		if err != nil {
			return nil, fmt.Errorf("%s: effort.outbound_model_template: %w", p.spec.Name, err)
		}
		d.outTmpl = tmpl
	}
	return d, nil
}

func (d *DeclaredProvider) KeepsCacheOnChange(key string, snap form.Snapshot) bool {
	if d.spec.Effort.KeepCache && (key == "system.thinking_effort" || key == "system.thinking_budget") {
		return true
	}
	return d.Provider.KeepsCacheOnChange(key, snap)
}

func (d *DeclaredProvider) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	listed, err := d.Provider.Models(ctx)
	if err != nil || d.mapRe == nil {
		return listed, err
	}
	out := make([]provider.ModelInfo, 0, len(listed))
	idxByID := make(map[string]int, len(listed))
	matched := make(map[string]bool)
	rungs := make(map[string][]string)
	for _, m := range listed {
		id := m.ID
		if sub := d.mapRe.FindStringSubmatch(m.ID); sub != nil && d.modelIdx >= 0 && sub[d.modelIdx] != "" {
			id = sub[d.modelIdx]
			matched[id] = true
			if d.effortIdx >= 0 {
				if effort := strings.ToLower(strings.TrimSpace(sub[d.effortIdx])); effort != "" {
					if !slices.Contains(rungs[id], effort) {
						rungs[id] = append(rungs[id], effort)
					}
					m.Entry.Name = stripEffortTitle(m.Entry.Name, effort)
					yes := true
					if m.Entry.Reasoning == nil {
						m.Entry.Reasoning = &yes
					}
				}
			}
			m.ID = id
		}
		if idx, exists := idxByID[id]; exists {
			out[idx].Entry = mergeModelEntry(out[idx].Entry, m.Entry)
		} else {
			idxByID[id] = len(out)
			out = append(out, m)
		}
	}
	for id, rs := range rungs {
		sort.Slice(rs, func(i, j int) bool { return d.rungScore(rs[i]) < d.rungScore(rs[j]) })
		rungs[id] = rs
	}
	d.shared.store(matched, rungs)
	d.cat.mu.Lock()
	for _, m := range out {
		if _, exists := d.cat.entries[m.ID]; !exists {
			d.cat.entries[m.ID] = m.Entry
		}
	}
	d.cat.mu.Unlock()
	return out, nil
}

func (d *DeclaredProvider) Send(ctx context.Context, in provider.SendInput, bus provider.Bus) error {
	model := snapshotString(in.Snapshot, "system.model")
	if model == "" {
		d.mu.RLock()
		model = d.model
		d.mu.RUnlock()
	}
	if !d.spec.CatalogOff && ((d.mapRe != nil && !d.shared.populated()) || (d.outTmpl != nil && d.cat.empty())) &&
		(d.spec.Catalog != "" || d.spec.Default == Gemini || d.spec.Default == CodeAssist) {
		cctx, cancel := context.WithTimeout(ctx, catalogTimeout)
		_, _ = d.Models(cctx)
		cancel()
	}
	inlineEffort := ""
	rungs, matched := d.shared.lookup(model)
	if d.mapRe != nil && d.modelIdx >= 0 {
		if sub := d.mapRe.FindStringSubmatch(model); sub != nil && sub[d.modelIdx] != "" {
			model = sub[d.modelIdx]
			if d.effortIdx >= 0 {
				inlineEffort = strings.ToLower(strings.TrimSpace(sub[d.effortIdx]))
			}
			rungs, _ = d.shared.lookup(model)
			matched = true
		}
	}
	rawEffort := snapshotEffort(in.Snapshot)
	if rawEffort == "" {
		rawEffort = d.spec.Effort.DefaultRung
	}
	if rawEffort == "" {
		rawEffort = inlineEffort
	}
	if effort, ok := d.resolveEffort(rawEffort, rungs, matched); ok {
		in.Snapshot = in.Snapshot.SetPath("system.thinking_effort", mustJSONString(effort))
		if d.outTmpl != nil && model != "" {
			var buf bytes.Buffer
			if err := d.outTmpl.Execute(&buf, struct{ Model, Effort string }{Model: model, Effort: effort}); err != nil {
				return fmt.Errorf("%s: outbound_model_template: %w", d.Name(), err)
			}
			outbound := strings.TrimSpace(buf.String())
			applyOutbound := d.mapRe == nil || matched || (!d.shared.populated() && d.mapRe.MatchString(outbound))
			if outbound != "" && applyOutbound {
				if outbound != model {
					if e, known := d.cat.lookup(model); known {
						d.cat.mu.Lock()
						if _, exists := d.cat.entries[outbound]; !exists {
							d.cat.entries[outbound] = e
						}
						d.cat.mu.Unlock()
					}
				}
				in.Snapshot = in.Snapshot.SetPath("system.model", mustJSONString(outbound))
			}
		}
	}
	return d.Provider.Send(ctx, in, bus)
}

var builtinRungScores = map[string]float64{
	"none":    0,
	"minimal": 10,
	"low":     25,
	"medium":  50,
	"high":    75,
	"xhigh":   95,
	"max":     100,
}

type scoredRung struct {
	name  string
	score float64
}

func (d *DeclaredProvider) knownScore(rung string) (float64, bool) {
	r := strings.ToLower(strings.TrimSpace(rung))
	if v, ok := d.spec.Effort.Ladder[r]; ok {
		return v, true
	}
	v, ok := builtinRungScores[r]
	return v, ok
}

func (d *DeclaredProvider) rungScore(rung string) float64 {
	if v, ok := d.knownScore(rung); ok {
		return v
	}
	return 50
}

func (d *DeclaredProvider) resolveEffort(raw string, observed []string, matched bool) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if n, err := strconv.ParseFloat(raw, 64); err == nil {
		n = max(0, min(100, n))
		var cands []scoredRung
		switch {
		case matched && len(observed) > 0:
			for _, r := range observed {
				cands = append(cands, scoredRung{name: r, score: d.rungScore(r)})
			}
		case len(d.spec.Effort.Ladder) > 0:
			for r, sc := range d.spec.Effort.Ladder {
				cands = append(cands, scoredRung{name: r, score: sc})
			}
		default:
			return "", false
		}
		return pickRung(n, cands, d.spec.Effort.Round), true
	}
	e := strings.ToLower(raw)
	if matched && len(observed) > 0 {
		if slices.Contains(observed, e) {
			return e, true
		}
		cands := make([]scoredRung, 0, len(observed))
		for _, r := range observed {
			cands = append(cands, scoredRung{name: r, score: d.rungScore(r)})
		}
		if sc, ok := d.knownScore(e); ok {
			return pickRung(sc, cands, d.spec.Effort.Round), true
		}
		sortRungs(cands)
		return cands[len(cands)-1].name, true
	}
	if len(d.spec.Effort.Ladder) > 0 {
		if _, ok := d.spec.Effort.Ladder[e]; ok {
			return e, true
		}
		if sc, ok := d.knownScore(e); ok {
			cands := make([]scoredRung, 0, len(d.spec.Effort.Ladder))
			for r, v := range d.spec.Effort.Ladder {
				cands = append(cands, scoredRung{name: r, score: v})
			}
			return pickRung(sc, cands, d.spec.Effort.Round), true
		}
	}
	return e, true
}

func sortRungs(cands []scoredRung) {
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score < cands[j].score
		}
		return cands[i].name < cands[j].name
	})
}

func pickRung(target float64, cands []scoredRung, mode RoundMode) string {
	sortRungs(cands)
	switch mode {
	case RoundDown:
		best := cands[0]
		for _, c := range cands {
			if c.score <= target {
				best = c
			}
		}
		return best.name
	case RoundUp:
		for _, c := range cands {
			if c.score >= target {
				return c.name
			}
		}
		return cands[len(cands)-1].name
	default:
		best := cands[0]
		bestDist := math.Abs(best.score - target)
		for _, c := range cands[1:] {
			dist := math.Abs(c.score - target)
			if dist < bestDist || (dist == bestDist && c.score > best.score) {
				best, bestDist = c, dist
			}
		}
		return best.name
	}
}

func snapshotEffort(snap form.Snapshot) string {
	raw, ok := snap.Get("system.thinking_effort")
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		if n == 0 {
			n = 0
		}
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	return ""
}

func mustJSONString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

func stripEffortTitle(name, effort string) string {
	lower := strings.ToLower(name)
	for _, suf := range []string{" (" + effort + ")", " (extra high)"} {
		if suf == " (extra high)" && effort != "xhigh" {
			continue
		}
		if strings.HasSuffix(lower, suf) {
			return strings.TrimSpace(name[:len(name)-len(suf)])
		}
	}
	return name
}

func mergeBoolPtr(a, b *bool) *bool {
	if a == nil && b == nil {
		return nil
	}
	v := (a != nil && *a) || (b != nil && *b)
	return &v
}

func mergeModelEntry(a, b apimodels.Entry) apimodels.Entry {
	if a.Name == "" && b.Name != "" {
		a.Name = b.Name
	}
	a.Context.Default = max(a.Context.Default, b.Context.Default)
	a.Context.Long = max(a.Context.Long, b.Context.Long)
	a.MaxOutput = max(a.MaxOutput, b.MaxOutput)
	a.MaxPrompt = max(a.MaxPrompt, b.MaxPrompt)
	a.ImageBytes = max(a.ImageBytes, b.ImageBytes)
	a.ImageCount = max(a.ImageCount, b.ImageCount)
	a.Images = mergeBoolPtr(a.Images, b.Images)
	a.Tools = mergeBoolPtr(a.Tools, b.Tools)
	a.Reasoning = mergeBoolPtr(a.Reasoning, b.Reasoning)
	a.Hidden = a.Hidden && b.Hidden
	for _, ep := range b.Endpoints {
		if !slices.Contains(a.Endpoints, ep) {
			a.Endpoints = append(a.Endpoints, ep)
		}
	}
	if len(b.Src) > 0 {
		if a.Src == nil {
			a.Src = make(map[string]string, len(b.Src))
		}
		for k, v := range b.Src {
			if _, ok := a.Src[k]; !ok {
				a.Src[k] = v
			}
		}
	}
	return a
}

type effortCache struct {
	mu      sync.RWMutex
	loaded  bool
	matched map[string]bool
	rungs   map[string][]string
}

var effortCaches sync.Map

func sharedEffortCache(name, base, mapModel string) *effortCache {
	c, _ := effortCaches.LoadOrStore(name+"\x00"+base+"\x00"+mapModel, &effortCache{})
	return c.(*effortCache)
}

func (c *effortCache) store(matched map[string]bool, rungs map[string][]string) {
	c.mu.Lock()
	c.loaded, c.matched, c.rungs = true, matched, rungs
	c.mu.Unlock()
}

func (c *effortCache) populated() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loaded
}

func (c *effortCache) lookup(model string) ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]string(nil), c.rungs[model]...), c.matched[model]
}
