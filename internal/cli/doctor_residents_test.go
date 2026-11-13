package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/rpc"
)

func TestDoctorResidentsSaysCountAndWhatOutlivedItsFigaro(t *testing.T) {
	rows := []rpc.Resident{{ID: "aaaa", Agent: true, Endpoint: true}, {ID: "bbbb", Endpoint: true}, {ID: "cccc", Agent: true}}
	var out bytes.Buffer
	require.NoError(t, printResidents(&out, rows, false))
	require.Equal(t, "3 resident(s) whose figaro the store does not hold\naaaa  agent and endpoint\nbbbb  endpoint\ncccc  agent\n", out.String())

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
