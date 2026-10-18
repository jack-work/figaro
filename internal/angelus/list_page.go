package angelus

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jack-work/figaro/api/rpc"
)

func listBounded(req rpc.ListRequest) bool {
	return req.Limit > 0 || req.Before != ""
}

func listOrder(a, b rpc.FigaroInfoResponse) int {
	switch {
	case a.LastActive != b.LastActive:
		if a.LastActive > b.LastActive {
			return -1
		}
		return 1
	case a.ID != b.ID:
		if a.ID < b.ID {
			return -1
		}
		return 1
	}
	return 0
}

func listCursor(r rpc.FigaroInfoResponse) string {
	return strconv.FormatInt(r.LastActive, 10) + ":" + r.ID
}

func parseListCursor(s string) (int64, string, error) {
	ms, id, ok := strings.Cut(s, ":")
	if !ok || id == "" {
		return 0, "", fmt.Errorf("list cursor %q: want <millis>:<id>", s)
	}
	millis, err := strconv.ParseInt(ms, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("list cursor %q: %w", s, err)
	}
	return millis, id, nil
}

func afterCursor(r rpc.FigaroInfoResponse, millis int64, id string) bool {
	return listOrder(r, rpc.FigaroInfoResponse{LastActive: millis, ID: id}) > 0
}

func boundPage(rows []rpc.FigaroInfoResponse, req rpc.ListRequest) ([]rpc.FigaroInfoResponse, string, error) {
	if !listBounded(req) {
		return rows, "", nil
	}
	slices.SortFunc(rows, listOrder)
	if req.Before != "" {
		millis, id, err := parseListCursor(req.Before)
		if err != nil {
			return nil, "", err
		}
		i, _ := slices.BinarySearchFunc(rows, rpc.FigaroInfoResponse{LastActive: millis, ID: id}, listOrder)
		for i < len(rows) && !afterCursor(rows[i], millis, id) {
			i++
		}
		rows = rows[i:]
	}
	if req.Limit > 0 && len(rows) > req.Limit {
		page := rows[:req.Limit:req.Limit]
		return page, listCursor(page[len(page)-1]), nil
	}
	return rows, "", nil
}

func pageEnrichments(page []rpc.FigaroInfoResponse, all []listEnrichment) []listEnrichment {
	want := make(map[string]struct{}, len(all))
	for _, e := range all {
		want[e.ariaID] = struct{}{}
	}
	out := make([]listEnrichment, 0, len(page))
	for i, r := range page {
		if _, ok := want[r.ID]; ok {
			out = append(out, listEnrichment{index: i, ariaID: r.ID})
		}
	}
	return out
}
