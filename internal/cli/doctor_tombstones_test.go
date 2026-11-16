package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

type fakeTombstoneDoor struct {
	figaros []rpc.FigaroInfoResponse
	forms   map[string]form.Snapshot
	killed  []string
}

func (d *fakeTombstoneDoor) ListGlobal(context.Context) (*rpc.ListResponse, error) {
	return &rpc.ListResponse{Figaros: d.figaros}, nil
}

func (d *fakeTombstoneDoor) Form(_ context.Context, id string) (*rpc.FormResponse, error) {
	return &rpc.FormResponse{Snapshot: d.forms[id]}, nil
}

func (d *fakeTombstoneDoor) Kill(_ context.Context, req rpc.KillRequest) (*rpc.KillResponse, error) {
	d.killed = append(d.killed, req.FigaroID)
	return &rpc.KillResponse{OK: true, PurgeAt: 1_700_000_000_000, Reaped: []string{req.FigaroID}}, nil
}

func TestDoctorTombstonesListsAndReapsSealedArias(t *testing.T) {
	sealed := form.FromMap(map[string]json.RawMessage{
		store.TombstoneKey: json.RawMessage(`"deleted"`),
	})
	healthy := form.FromMap(map[string]json.RawMessage{
		"mantra": json.RawMessage(`"working"`),
	})
	door := &fakeTombstoneDoor{
		figaros: []rpc.FigaroInfoResponse{
			{ID: "alive-aria", CreatedAt: 1000},
			{ID: "zombie-aria", CreatedAt: 2000},
			{ID: "@role-form", State: "form"},
		},
		forms: map[string]form.Snapshot{
			"alive-aria":  healthy,
			"zombie-aria": sealed,
		},
	}

	rows, err := collectTombstones(context.Background(), door, false)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "zombie-aria", rows[0].ID)
	require.Empty(t, door.killed)

	var out, errOut bytes.Buffer
	require.NoError(t, printTombstones(&out, &errOut, rows, true, true, false))
	require.Contains(t, out.String(), "would reap")

	rows, err = collectTombstones(context.Background(), door, true)
	require.NoError(t, err)
	require.Equal(t, []string{"zombie-aria"}, door.killed)
	require.True(t, rows[0].Reaped)

	out.Reset()
	errOut.Reset()
	require.NoError(t, printTombstones(&out, &errOut, rows, true, false, true))
	require.True(t, strings.Contains(out.String(), `"reaped": 1`))
}
