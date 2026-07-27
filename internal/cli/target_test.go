package cli

import (
	"reflect"
	"strings"
	"testing"
)

func TestExtractIDFlag(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		allowed []string // flags the calling verb consumes itself
		wantID  string
		wantOut []string
		wantErr string // substring; empty means "no error"
	}{
		{
			name:    "absent",
			in:      []string{"--", "hello world"},
			wantID:  "",
			wantOut: []string{"--", "hello world"},
		},
		{
			name:    "space form",
			in:      []string{"--id", "myaria", "--", "hello"},
			wantID:  "myaria",
			wantOut: []string{"--", "hello"},
		},
		{
			name:    "equals form",
			in:      []string{"--id=myaria", "--", "hello"},
			wantID:  "myaria",
			wantOut: []string{"--", "hello"},
		},
		{
			name:    "interleaved flags",
			in:      []string{"-n", "--id", "myaria", "-y", "--", "fmt files"},
			allowed: execOwnFlags,
			wantID:  "myaria",
			wantOut: []string{"-n", "-y", "--", "fmt files"},
		},
		{
			// The point of the fix: an unconsumed flag is an error, never
			// a silent drop. `figaro plain --nope -- hi` used to run with
			// the flag on the floor.
			name:    "unknown long flag",
			in:      []string{"--nope", "--", "hi"},
			wantErr: `unknown flag "--nope"`,
		},
		{
			name:    "unknown short flag",
			in:      []string{"-Z", "--", "hi"},
			wantErr: `unknown flag "-Z"`,
		},
		{
			// x's own flags are not magic to `plain`: without the
			// passthrough list they are unknown.
			name:    "exec flags rejected for plain",
			in:      []string{"-n", "--", "hi"},
			wantErr: `unknown flag "-n"`,
		},
		{
			// Bundles were never supported here; now they say so.
			name:    "bundled short flags are not a thing",
			in:      []string{"-ny", "--", "hi"},
			allowed: execOwnFlags,
			wantErr: `unknown flag "-ny"`,
		},
		{
			name:    "bare argument before --",
			in:      []string{"hello", "--", "hi"},
			wantErr: "the prompt must follow `--`",
		},
		{
			name:    "no -- boundary at all",
			in:      []string{"hello"},
			wantErr: "the prompt must follow `--`",
		},
		{
			// Guard: a `--` inside the prompt body is prompt text, not a
			// second boundary, and the scan stops at the first one.
			name:    "double dash inside the prompt",
			in:      []string{"--id", "myaria", "--", "text with -- inside", "--nope"},
			wantID:  "myaria",
			wantOut: []string{"--", "text with -- inside", "--nope"},
		},
		{
			name:    "id-like text in prompt is untouched",
			in:      []string{"--", "now do --id foo on the file"},
			wantID:  "",
			wantOut: []string{"--", "now do --id foo on the file"},
		},
		{
			name:    "no value before dash dash",
			in:      []string{"--id", "--", "hello"},
			wantErr: "--id requires a value",
		},
		{
			name:    "missing value at end",
			in:      []string{"--id"},
			wantErr: "--id requires a value",
		},
		{
			name:    "double specification",
			in:      []string{"--id", "a", "--id", "b", "--", "hi"},
			wantErr: "--id given more than once",
		},
		{
			name:    "invalid id",
			in:      []string{"--id", "has spaces and slashes /etc", "--"},
			wantErr: "--id",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotID, gotOut, err := extractIDFlagAllowing(tc.in, tc.allowed)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error, got id=%q out=%v", gotID, gotOut)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotID != tc.wantID {
				t.Errorf("id: got %q, want %q", gotID, tc.wantID)
			}
			if !reflect.DeepEqual(gotOut, tc.wantOut) {
				t.Errorf("out: got %v, want %v", gotOut, tc.wantOut)
			}
		})
	}
}
