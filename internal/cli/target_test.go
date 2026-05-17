package cli

import (
	"reflect"
	"testing"
)

func TestExtractIDFlag(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		wantID  string
		wantOut []string
		wantErr bool
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
			wantID:  "myaria",
			wantOut: []string{"-n", "-y", "--", "fmt files"},
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
			wantErr: true,
		},
		{
			name:    "missing value at end",
			in:      []string{"--id"},
			wantErr: true,
		},
		{
			name:    "double specification",
			in:      []string{"--id", "a", "--id", "b", "--", "hi"},
			wantErr: true,
		},
		{
			name:    "invalid id",
			in:      []string{"--id", "has spaces and slashes /etc", "--"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotID, gotOut, err := extractIDFlag(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got id=%q out=%v", gotID, gotOut)
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
