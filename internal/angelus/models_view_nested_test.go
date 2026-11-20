package angelus

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/api/rpc"
)

func TestTheModelsViewDrawsEveryDepthTheCatalogStores(t *testing.T) {
	view := formViews[models.FormID]
	require.Equal(t, models.BranchesKey, view.Via)
	for _, route := range [][]string{
		{"anthropic", "claude-opus-5"},
		{"rou2", "explabs", "glm-5.3"},
		{"rou2", "copilot", "anthropic", "claude-opus-5"},
		{"router", "a", "b", "c", "model"},
	} {
		path := models.Path(strings.Join(route[1:], "/"))
		segs := []string{route[0]}
		for _, up := range path[:len(path)-1] {
			segs = append(segs, models.BranchesKey, up)
		}
		segs = append(segs, models.LeavesKey, path[len(path)-1])
		_, row := rpc.MatchPath(view.Rows, segs)
		require.True(t, row, "%v is a row", route)
		for i := 1; i < len(segs)-1; i++ {
			if segs[i] == models.LeavesKey {
				continue
			}
			_, open := rpc.MatchPath(view.Open, segs[:i+1])
			require.True(t, open, "%v opens at %v", route, segs[:i+1])
		}
		_, node := rpc.MatchPath(view.Rows, segs[:len(segs)-1])
		require.False(t, node, "%v: the collection is not itself a row", route)
	}
}
