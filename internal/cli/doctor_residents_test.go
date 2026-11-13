package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
)

func TestDoctorResidentsSaysCountAndWhatOutlivedItsFigaro(t *testing.T) {
	rows := []rpc.Resident{{ID: "aaaa", Agent: true, Endpoint: true}, {ID: "bbbb", Endpoint: true}, {ID: "cccc", Agent: true}, {ID: "dddd", Sidecar: true}}
	var out bytes.Buffer
	require.NoError(t, printResidents(&out, rows, false))
	require.Equal(t, "4 resident(s) whose id the store does not hold\naaaa  agent, endpoint\nbbbb  endpoint\ncccc  agent\ndddd  _meta sidecar\n", out.String())

	out.Reset()
	require.NoError(t, printResidents(&out, nil, true))
	var got struct {
		Count     int            `json:"count"`
		Residents []rpc.Resident `json:"residents"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, 0, got.Count)
	require.NotNil(t, got.Residents, "-j gives an empty list, not null")
}
