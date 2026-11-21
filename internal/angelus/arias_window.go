package angelus

import (
	"slices"
	"strings"

	"github.com/jack-work/figaro/api/rpc"
)

const webWindowRows = 200

func newerFirst(a, b rpc.FigaroInfoResponse) int {
	switch {
	case a.LastActive > b.LastActive:
		return -1
	case a.LastActive < b.LastActive:
		return 1
	}
	return strings.Compare(a.ID, b.ID)
}

func newestRows(rows []rpc.FigaroInfoResponse, n int) ([]rpc.FigaroInfoResponse, int) {
	if len(rows) <= n {
		return rows, 0
	}
	kept := slices.Clone(rows)
	slices.SortStableFunc(kept, newerFirst)
	kept = kept[:n]
	return kept, len(rows) - len(kept)
}

func newestWithAncestors(rows []rpc.FigaroInfoResponse, n int) ([]rpc.FigaroInfoResponse, int) {
	newest, more := newestRows(rows, n)
	if more == 0 {
		return rows, 0
	}
	byID := make(map[string]*rpc.FigaroInfoResponse, len(rows))
	for i := range rows {
		byID[rows[i].ID] = &rows[i]
	}
	keep := make(map[string]bool, len(newest))
	for i := range newest {
		for r := &newest[i]; r != nil && !keep[r.ID]; r = byID[drawnUnder(r)] {
			keep[r.ID] = true
		}
	}
	var out []rpc.FigaroInfoResponse
	for _, r := range rows {
		if keep[r.ID] {
			out = append(out, r)
		}
	}
	return out, len(rows) - len(out)
}
