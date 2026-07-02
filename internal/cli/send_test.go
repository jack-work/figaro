package cli

import (
	"reflect"
	"strings"
	"testing"
)

func TestExtractSendFlags(t *testing.T) {
	cases := []struct {
		name     string
		in       []string
		wantOpts sendOpts
		wantRest []string
		wantErr  string
	}{
		{
			name:     "bare prompt",
			in:       []string{"--", "hello", "world"},
			wantOpts: sendOpts{},
			wantRest: []string{"--", "hello", "world"},
		},
		{
			name:     "json long",
			in:       []string{"--json", "--id", "x", "--", "hi"},
			wantOpts: sendOpts{id: "x", json: true},
			wantRest: []string{"--", "hi"},
		},
		{
			name:     "json short",
			in:       []string{"-j", "--id", "y", "--", "hi"},
			wantOpts: sendOpts{id: "y", json: true},
			wantRest: []string{"--", "hi"},
		},
		{
			name:     "forget + json",
			in:       []string{"-f", "-j", "--id", "z", "--", "hi"},
			wantOpts: sendOpts{id: "z", forget: true, json: true},
			wantRest: []string{"--", "hi"},
		},
		{
			name:     "id flag",
			in:       []string{"--id", "myid", "--", "hi"},
			wantOpts: sendOpts{id: "myid"},
			wantRest: []string{"--", "hi"},
		},
		{
			name:     "id equals form",
			in:       []string{"--id=myid", "--", "hi"},
			wantOpts: sendOpts{id: "myid"},
			wantRest: []string{"--", "hi"},
		},
		{
			name:     "ephemeral long",
			in:       []string{"--ephemeral", "--", "p"},
			wantOpts: sendOpts{ephemeral: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "ephemeral short",
			in:       []string{"-e", "--", "p"},
			wantOpts: sendOpts{ephemeral: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "exec short",
			in:       []string{"-x", "--", "p"},
			wantOpts: sendOpts{exec: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "bundled ex",
			in:       []string{"-ex", "--", "p"},
			wantOpts: sendOpts{ephemeral: true, exec: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "bundled er",
			in:       []string{"-er", "--", "p"},
			wantOpts: sendOpts{ephemeral: true, raw: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "raw long",
			in:       []string{"--raw", "--", "p"},
			wantOpts: sendOpts{raw: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "raw short",
			in:       []string{"-r", "--", "p"},
			wantOpts: sendOpts{raw: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "bundled exy",
			in:       []string{"-exy", "--", "p"},
			wantOpts: sendOpts{ephemeral: true, exec: true, skipYes: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "forget long",
			in:       []string{"--forget", "--", "p"},
			wantOpts: sendOpts{forget: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "forget short",
			in:       []string{"-f", "--", "p"},
			wantOpts: sendOpts{forget: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "bundled rf",
			in:       []string{"-rf", "--", "p"},
			wantOpts: sendOpts{raw: true, forget: true},
			wantRest: []string{"--", "p"},
		},
		{
			name:     "dry-run",
			in:       []string{"-x", "-n", "--", "ls"},
			wantOpts: sendOpts{exec: true, dryRun: true},
			wantRest: []string{"--", "ls"},
		},
		{
			name:     "flags after id flag",
			in:       []string{"--id", "foo", "-x", "--", "ls"},
			wantOpts: sendOpts{id: "foo", exec: true},
			wantRest: []string{"--", "ls"},
		},
		{
			name:    "missing id value",
			in:      []string{"--id", "--", "p"},
			wantErr: "--id requires a value",
		},
		{
			name:    "duplicate id",
			in:      []string{"--id", "a", "--id", "b", "--", "p"},
			wantErr: "more than once",
		},
		{
			name:    "invalid id char",
			in:      []string{"--id", "bad/id", "--", "p"},
			wantErr: "--id",
		},
		{
			name:     "no -- boundary",
			in:       []string{"-e", "hello"},
			wantOpts: sendOpts{ephemeral: true},
			wantRest: []string{"hello"},
		},
		{
			name:     "flags ignored after --",
			in:       []string{"-e", "--", "-x", "should", "be", "prompt"},
			wantOpts: sendOpts{ephemeral: true},
			wantRest: []string{"--", "-x", "should", "be", "prompt"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotOpts, gotRest, err := extractSendFlags(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotOpts != tc.wantOpts {
				t.Errorf("opts: got %+v, want %+v", gotOpts, tc.wantOpts)
			}
			if !reflect.DeepEqual(gotRest, tc.wantRest) {
				t.Errorf("rest: got %v, want %v", gotRest, tc.wantRest)
			}
		})
	}
}

func TestParseSendTarget(t *testing.T) {
	cases := []struct {
		spec    string
		trunk   string
		lt      uint64
		hasLT   bool
		wantErr bool
	}{
		{"", "", 0, false, false},
		{":6", "", 6, true, false},
		{"t1:6", "t1", 6, true, false},
		{"t1", "t1", 0, false, false},
		{":", "", 0, false, true},
		{"t1:x", "", 0, false, true},
	}
	for _, c := range cases {
		trunk, lt, hasLT, err := parseSendTarget(c.spec)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: err=%v wantErr=%v", c.spec, err, c.wantErr)
			continue
		}
		if c.wantErr {
			continue
		}
		if trunk != c.trunk || lt != c.lt || hasLT != c.hasLT {
			t.Errorf("%q: got (%q,%d,%v) want (%q,%d,%v)", c.spec, trunk, lt, hasLT, c.trunk, c.lt, c.hasLT)
		}
	}
}
