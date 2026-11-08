package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

type fakeQuarantineDoor struct {
	confirmed []rpc.QuarantineReleaseRequest
}

func (f *fakeQuarantineDoor) QuarantineList(context.Context) (*rpc.QuarantineListResponse, error) {
	return &rpc.QuarantineListResponse{Entries: []rpc.QuarantineEntry{{Figaro: "abc", Channel: "ir", Segment: "/s/3.jsonl", Offset: 171}}}, nil
}

func (f *fakeQuarantineDoor) QuarantineRelease(_ context.Context, req rpc.QuarantineReleaseRequest) (*rpc.QuarantineReleaseResponse, error) {
	resp := &rpc.QuarantineReleaseResponse{QuarantineEntry: rpc.QuarantineEntry{Figaro: req.Figaro, Channel: "ir", Segment: "/s/3.jsonl", Offset: req.At},
		BytesCut: 400, Aside: "/q/abc-ir-3", Applied: req.Confirm}
	if req.Confirm {
		f.confirmed = append(f.confirmed, req)
	}
	return resp, nil
}

// A release cuts records off a figaro's history; it happens only when the
// owner says so, by --yes or by typing back the offset at a terminal.
func TestAQuarantineReleaseCutsOnlyOnExplicitConfirmation(t *testing.T) {
	cases := []struct {
		name        string
		yes, tty    bool
		typed       string
		wantRelease bool
	}{
		{"no terminal, no --yes", false, false, "", false},
		{"wrong offset typed", false, true, "17\n", false},
		{"y is not the offset", false, true, "y\n", false},
		{"the offset typed back", false, true, "171\n", true},
		{"--yes", true, false, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &fakeQuarantineDoor{}
			var out bytes.Buffer
			err := doctorQuarantine(context.Background(), d, []string{"release", "abc"}, "171", "",
				quarantineOpts{Yes: tc.yes, Interactive: tc.tty, In: strings.NewReader(tc.typed), Out: &out})
			if got := len(d.confirmed) == 1; got != tc.wantRelease {
				t.Fatalf("released=%v, want %v (err %v)\n%s", got, tc.wantRelease, err, out.String())
			}
			if !strings.Contains(out.String(), "/s/3.jsonl") || !strings.Contains(out.String(), "171") || !strings.Contains(out.String(), "/q/abc-ir-3") {
				t.Fatalf("the plan does not name the segment, offset and where the copy goes:\n%s", out.String())
			}
			if !tc.wantRelease && err == nil {
				t.Fatal("a release that did not happen reported success")
			}
		})
	}
	if err := doctorQuarantine(context.Background(), &fakeQuarantineDoor{}, []string{"release", "abc"}, "", "", quarantineOpts{Yes: true, Out: &bytes.Buffer{}}); err == nil {
		t.Fatal("a release without --at was accepted")
	}
}
