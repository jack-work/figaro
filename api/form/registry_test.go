package form_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func TestTheRegistryHasOneRowPerPath(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range form.Registry() {
		if s.Path == "" {
			t.Errorf("a row has no path: %+v", s)
		}
		if seen[s.Path] {
			t.Errorf("%s is registered twice", s.Path)
		}
		seen[s.Path] = true
	}
}

func TestNoRowSitsUnderARowOfAnotherWriter(t *testing.T) {
	rows := form.Registry()
	for _, s := range rows {
		for _, o := range rows {
			if o.Path == s.Path || o.Exact {
				continue
			}
			literal := strings.NewReplacer("<name>", "x", "<id>", "x").Replace(s.Path)
			if (literal == o.Path || strings.HasPrefix(literal, o.Path+".") || strings.HasPrefix(literal, o.Path+"[")) && o.Writer != s.Writer {
				t.Errorf("%s (%s) is under %s (%s): a key has one writer class", s.Path, s.Writer, o.Path, o.Writer)
			}
		}
	}
}

func TestRuntimeKeysAreExactAndNothingElseIs(t *testing.T) {
	for _, s := range form.Registry() {
		if s.Exact != (s.Writer == form.WriterRuntime) {
			t.Errorf("%s: writer %s, exact %v", s.Path, s.Writer, s.Exact)
		}
	}
}

func TestEveryRowIsDocumentedOrGroupsDocumentedRows(t *testing.T) {
	rows := form.Registry()
	for _, s := range rows {
		if s.Short != "" {
			continue
		}
		grouped := false
		for _, o := range rows {
			if o.Path != s.Path && o.Short != "" && strings.HasPrefix(o.Path, s.Path+".") {
				grouped = true
			}
		}
		if !grouped {
			t.Errorf("%s has no description and groups nothing", s.Path)
		}
	}
}

func TestTheNamedKeysCarryTheirWriterClass(t *testing.T) {
	for want, keys := range map[form.Writer][]string{
		form.WriterLineage: {form.ScopesKey, form.RetiredGrantsKey, form.FanoutMaxChildrenKey, form.FanoutMintKey, "system.sandbox"},
		form.WriterRuntime: {form.FailureReasonKey, form.FailureCategoryKey, form.FailureTurnKey, form.FailureAtKey, "system.forked_from", "system.studies", "system.tombstone", "system.checkpoint.marks", "system.succession_warned"},
		form.WriterOwner:   {"system.provider", "system.model", "system.ttl", "system.retired", "system.retention.mode", "system.methods.pick.key"},
		form.WriterSelf:    {"system.cwd", "system.cache_control", "system.checkpoint.every_patches", "mantra", "datetime", "system.fanout"},
	} {
		for _, k := range keys {
			if got := form.WriterOf(k); got != want {
				t.Errorf("%s: writer %s, want %s", k, got, want)
			}
		}
	}
}

func TestARuntimeKeyIsRefusedToAnUnprivilegedPatchAndItsChildrenAreNot(t *testing.T) {
	for _, s := range form.Registry() {
		if s.Writer != form.WriterRuntime {
			continue
		}
		set := form.Build(form.Snapshot{}, map[string]json.RawMessage{s.Path: json.RawMessage(`1`)}, nil)
		if err := form.CheckWritable(set, false); err == nil || !strings.Contains(err.Error(), s.Path) {
			t.Errorf("%s: set refused with %v", s.Path, err)
		}
		if err := form.CheckWritable(set, true); err != nil {
			t.Errorf("%s: a privileged write was refused: %v", s.Path, err)
		}
		if err := form.CheckWritable(form.Build(form.Snapshot{}, nil, []string{s.Path}), false); err == nil {
			t.Errorf("%s: removal passed", s.Path)
		}
	}
}

func TestTheClassListsAreTheRegistryRoots(t *testing.T) {
	for w, got := range map[form.Writer][]string{
		form.WriterOwner:   form.OwnerHeldKeys(),
		form.WriterLineage: form.ProtectedKeys(),
	} {
		want := form.RootsOf(w)
		if strings.Join(got, ",") != strings.Join(want, ",") || len(want) == 0 {
			t.Errorf("%s: %v, want %v", w, got, want)
		}
	}
	for _, k := range form.OwnerHeldKeys() {
		if strings.Contains(k, "<") {
			t.Errorf("%s is a placeholder, not a key", k)
		}
	}
	for _, k := range form.RootsOf(form.WriterOwner) {
		if strings.HasPrefix(k, "system.methods.") || strings.HasPrefix(k, "system.retention.") {
			t.Errorf("%s is listed beside the root that covers it", k)
		}
	}
}
