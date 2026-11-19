package approvals

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jack-work/figaro/api/form"
)

const (
	ElicitKey       = "elicit"
	VerbFigaroModel = "figaro.model"
	VerbFigaroTTL   = "figaro.ttl"
	VerbRoleModel   = "role.model"
	VerbRoleOutfits = "role.outfits"
	MaxExpiry       = 24 * time.Hour
)

var (
	ErrUnknownVerb = errors.New("unknown verb")
	ErrInvalid     = errors.New("invalid request")
	ErrExpired     = errors.New("request expired")
	ErrNoRequest   = errors.New("no such request")
	ErrHashChanged = errors.New("the patch changed since the page was rendered")
)

var (
	idRe       = regexp.MustCompile(`^[a-z0-9]{1,16}$`)
	ariaRe     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	roleRe     = regexp.MustCompile(`^@[0-9a-f]{8,32}$`)
	providerRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	modelRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	ttlRe      = regexp.MustCompile(`^(0|[1-9][0-9]{0,4}[smhdw])$`)
	outfitRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

type Forms interface {
	Read(ctx context.Context, id string) (form.Snapshot, uint64, error)
	Set(ctx context.Context, id string, set map[string]json.RawMessage, remove []string, ifVersion uint64) error
	Outfit(ctx context.Context, name string) (digest string, ok bool, err error)
}

type Request struct {
	Verb    string                     `json:"verb"`
	Target  string                     `json:"target"`
	Set     map[string]json.RawMessage `json:"set"`
	Reason  string                     `json:"reason"`
	Expires string                     `json:"expires"`
	State   string                     `json:"state"`
}

type Line struct {
	Key     string          `json:"key"`
	Current json.RawMessage `json:"current"`
	New     json.RawMessage `json:"new"`
}

type Plan struct {
	Filing        string            `json:"filing"`
	FilingVersion uint64            `json:"filing_version"`
	ID            string            `json:"id"`
	Verb          string            `json:"verb"`
	Target        string            `json:"target"`
	TargetVersion uint64            `json:"target_version"`
	Lines         []Line            `json:"lines"`
	Inputs        map[string]string `json:"inputs,omitempty"`
	Applied       bool              `json:"applied"`
	Hash          string            `json:"-"`
}

type Pending struct {
	Filing  string
	ID      string
	Request Request
	Raw     string
	Plan    *Plan
	Refusal string
}

func List(ctx context.Context, f Forms, filings []string, now time.Time) ([]Pending, error) {
	var out []Pending
	for _, filing := range filings {
		snap, _, err := f.Read(ctx, filing)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filing, err)
		}
		entries, err := elicitOf(snap)
		if err != nil {
			out = append(out, Pending{Filing: filing, Refusal: err.Error()})
			continue
		}
		ids := make([]string, 0, len(entries))
		for id := range entries {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			p := Pending{Filing: filing, ID: id, Raw: string(entries[id])}
			_ = json.Unmarshal(entries[id], &p.Request)
			plan, err := Build(ctx, f, filing, id, now)
			if err != nil {
				p.Refusal = err.Error()
			} else {
				p.Plan = &plan
			}
			out = append(out, p)
		}
	}
	return out, nil
}

func elicitOf(snap form.Snapshot) (map[string]json.RawMessage, error) {
	raw, ok := snap.Get(ElicitKey)
	if !ok {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%w: %s is not an object", ErrInvalid, ElicitKey)
	}
	return m, nil
}

func Build(ctx context.Context, f Forms, filing, id string, now time.Time) (Plan, error) {
	if !idRe.MatchString(id) || id == "denied" {
		return Plan{}, fmt.Errorf("%w: request id %q", ErrInvalid, id)
	}
	snap, version, err := f.Read(ctx, filing)
	if err != nil {
		return Plan{}, err
	}
	entries, err := elicitOf(snap)
	if err != nil {
		return Plan{}, err
	}
	raw, ok := entries[id]
	if !ok {
		return Plan{}, ErrNoRequest
	}
	var req Request
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return Plan{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := checkExpiry(req.Expires, now); err != nil {
		return Plan{}, err
	}
	t, ok := templates[req.Verb]
	if !ok {
		return Plan{}, fmt.Errorf("%w %q", ErrUnknownVerb, req.Verb)
	}
	if !t.target.MatchString(req.Target) {
		return Plan{}, fmt.Errorf("%w: target %q is not a %s", ErrInvalid, req.Target, t.targetKind)
	}
	if err := exactKeys(req.Set, t.keys); err != nil {
		return Plan{}, err
	}
	values, inputs, err := t.validate(ctx, f, req.Set)
	if err != nil {
		return Plan{}, err
	}
	tsnap, tversion := snap, version
	if req.Target != filing {
		if tsnap, tversion, err = f.Read(ctx, req.Target); err != nil {
			return Plan{}, fmt.Errorf("read target %s: %w", req.Target, err)
		}
	}
	p := Plan{Filing: filing, FilingVersion: version, ID: id, Verb: req.Verb, Target: req.Target,
		TargetVersion: tversion, Inputs: inputs, Applied: true}
	for _, k := range t.keys {
		cur, _ := tsnap.Get(k)
		cur = canonical(cur)
		p.Lines = append(p.Lines, Line{Key: k, Current: cur, New: values[k]})
		if !bytes.Equal(cur, values[k]) {
			p.Applied = false
		}
	}
	p.Hash = hashOf(p)
	return p, nil
}

func hashOf(p Plan) string {
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func checkExpiry(s string, now time.Time) error {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return fmt.Errorf("%w: expires %q is not RFC 3339", ErrInvalid, s)
	}
	if !t.After(now) {
		return ErrExpired
	}
	if t.Sub(now) > MaxExpiry {
		return fmt.Errorf("%w: expires more than %s out", ErrInvalid, MaxExpiry)
	}
	return nil
}

func exactKeys(set map[string]json.RawMessage, want []string) error {
	got := make([]string, 0, len(set))
	for k := range set {
		got = append(got, k)
	}
	sort.Strings(got)
	w := slices.Clone(want)
	sort.Strings(w)
	if !slices.Equal(got, w) {
		return fmt.Errorf("%w: set must name exactly %s, got %s", ErrInvalid, strings.Join(w, ", "), strings.Join(got, ", "))
	}
	return nil
}

func canonical(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	b, _ := json.Marshal(v)
	return b
}

func plainString(raw json.RawMessage, re *regexp.Regexp, field string) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%w: %s must be a string", ErrInvalid, field)
	}
	for _, r := range s {
		if r > unicode.MaxASCII || unicode.IsControl(r) {
			return "", fmt.Errorf("%w: %s holds a non-ASCII or control character", ErrInvalid, field)
		}
	}
	if !re.MatchString(s) {
		return "", fmt.Errorf("%w: %s %q is not allowed", ErrInvalid, field, s)
	}
	return s, nil
}
