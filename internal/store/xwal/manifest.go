package xwal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jack-work/figaro/internal/store/disk"
)

// The manifest is the only crash-safe enumeration of channels: names may be
// nested paths, so directory position alone cannot tell a channel dir from a
// grouping dir.
const manifestName = "xwal.json"
const channelPendingName = ".xwal-channel-pending"

type manifest struct {
	Main     string            `json:"main"`
	Codec    string            `json:"codec"`
	Channels []manifestChannel `json:"channels"`
	// Layout is the on-disk TOPOLOGY version: 4 is flat, absent (0) is the
	// nested v3 layout this build cannot read. It is stamped on creation and
	// by Flatten, and openTrunks refuses anything else.
	Layout           int  `json:"layout,omitempty"`
	UnstampedRecords bool `json:"unstamped_records,omitempty"`
}

// layoutVersion is the flat layout: every node at depth 1, lineage in .node.
const layoutVersion = 4

type manifestChannel struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Reducer string `json:"reducer,omitempty"`
	Opaque  bool   `json:"opaque,omitempty"`
	Unkeyed bool   `json:"unkeyed,omitempty"`
}

// manifest is the one place a spec becomes its on-disk form.
func (c ChannelSpec) manifest() manifestChannel {
	return manifestChannel{
		Name:    c.Name,
		Kind:    c.Kind.String(),
		Reducer: c.Reducer,
		Opaque:  c.Opaque,
		Unkeyed: c.Unkeyed,
	}
}

type channelPendingPlan struct {
	Channel manifestChannel `json:"channel"`
}

func loadOrCreateManifest(dir string, cfg Config) (manifest, error) {
	path := filepath.Join(dir, manifestName)
	if err := recoverAtomicReplacement(path); err != nil {
		return manifest{}, err
	}
	data, err := os.ReadFile(path)
	if err == nil {
		var m manifest
		if jerr := json.Unmarshal(data, &m); jerr != nil {
			return manifest{}, fmt.Errorf("xwal: parse manifest: %w", jerr)
		}
		return m, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return manifest{}, err
	}
	if cfg.Main == "" || len(cfg.Channels) == 0 {
		return manifest{}, fmt.Errorf("xwal: no manifest at %s and no Config to create one", dir)
	}
	// Refuse to invent a manifest for a store that already exists: a fresh
	// one would drop every channel the caller does not declare and claim a
	// layout nothing has verified.
	if hasChannelContent(filepath.Join(dir, cfg.Main)) {
		return manifest{}, fmt.Errorf(
			"xwal: %s holds data in %q but no %s; refusing to invent one, because a fresh "+
				"manifest would drop every channel not currently declared and claim a layout "+
				"nothing has verified",
			dir, cfg.Main, manifestName)
	}
	codecName := cfg.Codec
	if codecName == "" {
		codecName = "jsonl"
	}
	if _, err := codecByName(codecName); err != nil {
		return manifest{}, err
	}
	m := manifest{Main: cfg.Main, Codec: codecName, Layout: layoutVersion}
	seenMain := false
	seen := map[string]struct{}{}
	for _, c := range cfg.Channels {
		if _, ok := seen[c.Name]; ok {
			return manifest{}, fmt.Errorf("xwal: duplicate channel %q", c.Name)
		}
		if err := validateChannelSpec(dir, cfg, m, c); err != nil {
			return manifest{}, err
		}
		seen[c.Name] = struct{}{}
		if c.Name == cfg.Main {
			seenMain = true
		}
		if c.Kind == ChannelReducible && c.Reducer == "" {
			return manifest{}, fmt.Errorf("xwal: reducible channel %q needs a reducer name", c.Name)
		}
		m.Channels = append(m.Channels, c.manifest())
	}
	if !seenMain {
		return manifest{}, fmt.Errorf("xwal: main channel %q not in Channels", cfg.Main)
	}
	if err := prepareInitialChannels(dir, cfg, m); err != nil {
		return manifest{}, err
	}
	if err := writeManifest(dir, m); err != nil {
		return manifest{}, err
	}
	return m, nil
}

func prepareInitialChannels(dir string, cfg Config, m manifest) error {
	codec, err := codecByName(m.Codec)
	if err != nil {
		return err
	}
	for _, mc := range m.Channels {
		chDir := filepath.Join(dir, mc.Name)
		if err := mkdirAllSynced(chDir); err != nil {
			return err
		}
		if mc.Kind != ChannelReducible.String() {
			continue
		}
		reducer, ok := resolveReducer(cfg, mc.Reducer, mc.Name)
		if !ok || reducer.Reduce == nil {
			return fmt.Errorf("xwal: no reducer %q registered for channel %q", mc.Reducer, mc.Name)
		}
		if err := ensureWatermark(chDir, 1, codec, reducer.Initial); err != nil {
			return err
		}
	}
	return nil
}

func validateChannelSpec(root string, cfg Config, man manifest, spec ChannelSpec) error {
	nativeName := filepath.FromSlash(spec.Name)
	if spec.Name == "" || filepath.IsAbs(nativeName) || filepath.Clean(nativeName) != nativeName ||
		nativeName == "." || nativeName == ".." ||
		strings.HasPrefix(nativeName, ".."+string(filepath.Separator)) {
		return fmt.Errorf("xwal: invalid channel name %q", spec.Name)
	}
	for _, component := range strings.FieldsFunc(nativeName, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if strings.HasPrefix(component, ".") ||
			component == manifestName ||
			strings.HasSuffix(component, ".tmp") ||
			strings.HasSuffix(component, ".invalid") ||
			strings.HasSuffix(component, ".replace-pending") {
			return fmt.Errorf("xwal: reserved channel path component %q", component)
		}
	}
	switch spec.Kind {
	case ChannelLog:
		if spec.Reducer != "" {
			return fmt.Errorf("xwal: log channel %q cannot name reducer %q", spec.Name, spec.Reducer)
		}
	case ChannelReducible:
		if spec.Reducer == "" {
			return fmt.Errorf("xwal: reducible channel %q needs a reducer name", spec.Name)
		}
		reducer, ok := resolveReducer(cfg, spec.Reducer, spec.Name)
		if !ok || reducer.Reduce == nil {
			return fmt.Errorf("xwal: no reducer %q registered for channel %q", spec.Reducer, spec.Name)
		}
		codec, err := codecByName(man.Codec)
		if err != nil {
			return err
		}
		if _, err := codec.Hash(reducer.Initial); err != nil {
			return fmt.Errorf("xwal: invalid initial state for channel %q: %w", spec.Name, err)
		}
	default:
		return fmt.Errorf("xwal: invalid kind %d for channel %q", spec.Kind, spec.Name)
	}
	path := filepath.Join(root, spec.Name)
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return fmt.Errorf("xwal: channel path %q is not a directory", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, existing := range man.Channels {
		if existing.Name != spec.Name {
			continue
		}
		if existing.Kind != spec.Kind.String() ||
			existing.Reducer != spec.Reducer ||
			existing.Opaque != spec.Opaque {
			return fmt.Errorf("xwal: channel %q already exists as kind=%s reducer=%q opaque=%t",
				spec.Name, existing.Kind, existing.Reducer, existing.Opaque)
		}
	}
	return nil
}

func withChannelSpec(cfg Config, spec ChannelSpec) Config {
	channels := append([]ChannelSpec(nil), cfg.Channels...)
	for i := range channels {
		if channels[i].Name == spec.Name {
			channels[i] = spec
			cfg.Channels = channels
			return cfg
		}
	}
	cfg.Channels = append(channels, spec)
	return cfg
}

func writeManifest(dir string, m manifest) error {
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, manifestName)
	tmp := path + ".tmp"
	if err := writeSyncedFile(tmp, body); err != nil {
		return err
	}
	if err := atomicReplaceFile(tmp, path); err != nil {
		return err
	}
	return disk.SyncDir(dir)
}

func writeChannelPending(root string, plan channelPendingPlan) error {
	body, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	path := filepath.Join(root, channelPendingName)
	tmp := path + ".tmp"
	if err := writeSyncedFile(tmp, body); err != nil {
		return err
	}
	if err := atomicReplaceFile(tmp, path); err != nil {
		return err
	}
	return disk.SyncDir(root)
}

func readChannelPending(root string) (channelPendingPlan, bool, error) {
	path := filepath.Join(root, channelPendingName)
	if err := recoverAtomicReplacement(path); err != nil {
		return channelPendingPlan{}, false, err
	}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return channelPendingPlan{}, false, nil
	}
	if err != nil {
		return channelPendingPlan{}, false, err
	}
	var plan channelPendingPlan
	if err := json.Unmarshal(body, &plan); err != nil {
		return channelPendingPlan{}, false, err
	}
	return plan, true, nil
}

func removeChannelPending(root string) error {
	if err := os.Remove(filepath.Join(root, channelPendingName)); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return err
	}
	return disk.SyncDir(root)
}

func recoverChannelPending(root string, cfg Config, man manifest) (manifest, error) {
	plan, pending, err := readChannelPending(root)
	if err != nil || !pending {
		return man, err
	}
	if err := validChannelName(plan.Channel.Name); err != nil {
		return man, fmt.Errorf("xwal: pending channel: %w", err)
	}
	published := false
	for _, existing := range man.Channels {
		if existing.Name == plan.Channel.Name {
			if existing != plan.Channel {
				return man, fmt.Errorf("xwal: pending channel %q conflicts with manifest", existing.Name)
			}
			published = true
			break
		}
	}
	if !published {
		if err := os.RemoveAll(filepath.Join(root, plan.Channel.Name)); err != nil {
			return man, err
		}
		if err := rootChannel(root, cfg, man, plan.Channel); err != nil {
			return man, err
		}
		man.Channels = append(man.Channels, plan.Channel)
		if err := writeManifest(root, man); err != nil {
			return man, err
		}
	}
	if err := removeChannelPending(root); err != nil {
		return man, err
	}
	return man, nil
}

func channelFromManifest(cfg Config, mc manifestChannel) (*channel, error) {
	ch := &channel{name: mc.Name, kind: ChannelLog, rname: mc.Reducer, opaque: mc.Opaque}
	if mc.Kind != ChannelReducible.String() {
		return ch, nil
	}
	ch.kind = ChannelReducible
	reducer, ok := resolveReducer(cfg, ch.rname, ch.name)
	if !ok || reducer.Reduce == nil {
		return nil, fmt.Errorf("xwal: no reducer %q registered for channel %q", ch.rname, ch.name)
	}
	ch.reduce = reducer.Reduce
	ch.initial = reducer.Initial
	return ch, nil
}

// reconcileChannelProps brings an existing manifest's per-channel properties
// up to what the caller declares: materializeManifestChannels only adds
// missing directories, so a property introduced after a store was created
// never reached it.
func reconcileChannelProps(dir string, cfg Config, man manifest) (manifest, error) {
	declared := make(map[string]ChannelSpec, len(cfg.Channels))
	for _, c := range cfg.Channels {
		declared[c.Name] = c
	}
	changed := false
	for i, mc := range man.Channels {
		want, ok := declared[mc.Name]
		if !ok {
			continue // the caller says nothing about this channel
		}
		if want.Unkeyed && !mc.Unkeyed {
			man.Channels[i].Unkeyed = true
			changed = true
		}
		if mc.Unkeyed && !want.Unkeyed {
			return man, fmt.Errorf(
				"xwal: channel %q is unkeyed on disk but the caller declares it keyed; "+
					"its records carry no main LT and there is no converter that could invent one",
				mc.Name)
		}
		if want.Opaque != mc.Opaque {
			man.Channels[i].Opaque = want.Opaque
			changed = true
		}
		// Kind is refused, not adopted: Unkeyed and Opaque describe how a
		// record is written and mixed records read back correctly; Kind
		// describes what the records are, and neither direction has a converter.
		if want.Kind.String() != mc.Kind {
			return man, fmt.Errorf(
				"xwal: channel %q is %q on disk but the caller declares %q; "+
					"a channel's kind cannot be reinterpreted without a converter",
				mc.Name, mc.Kind, want.Kind.String())
		}
	}
	if !changed {
		return man, nil
	}
	if err := writeManifest(dir, man); err != nil {
		return man, err
	}
	return man, nil
}

// hasChannelContent: a node directory or a segment.
func hasChannelContent(dir string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), ".") {
			return true
		}
	}
	return false
}

func materializeManifestChannels(root string, cfg Config, man manifest) (manifest, error) {
	for _, mc := range man.Channels {
		if mc.Name == man.Main || pathExists(filepath.Join(root, mc.Name)) {
			continue
		}
		if err := rootChannel(root, cfg, man, mc); err != nil {
			return man, err
		}
	}
	return man, nil
}

// readManifestFile is the read-only load; loadOrCreateManifest is the writing path.
func readManifestFile(dir string) (manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return manifest{}, fmt.Errorf("xwal: read manifest: %w", err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return manifest{}, fmt.Errorf("xwal: parse manifest: %w", err)
	}
	return m, nil
}

// channelNames: each maps directly to a dir under the root.
func channelNames(dir string) ([]string, error) {
	m, err := readManifestFile(dir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(m.Channels))
	for _, c := range m.Channels {
		out = append(out, c.Name)
	}
	return out, nil
}

func mainChannelName(dir string) (string, error) {
	m, err := readManifestFile(dir)
	if err != nil {
		return "", err
	}
	return m.Main, nil
}
