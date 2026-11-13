package angelus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/jack-work/figaro/internal/workerpool"
)

const (
	searchTimeout   = 10 * time.Second
	searchAnswerMax = 256 << 10
)

func searchClient(wire http.RoundTripper) *http.Client {
	return &http.Client{
		Transport:     wire,
		Timeout:       searchTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func poolOfLeasedBox(loaded *config.Loaded, box string) (config.PoolConfig, bool) {
	for _, name := range loaded.Pools() {
		p, _ := loaded.Pool(name)
		if leasedBoxOfWorker(box, p.WorkerNames()) {
			return p, true
		}
	}
	return config.PoolConfig{}, false
}

func (h *handlers) searchAs(ctx context.Context, box, figaroID, query string, count int) ([]tool.SearchResult, error) {
	workers := h.angelus.Workers
	if workers == nil {
		return nil, errors.New("search: this daemon leases nothing")
	}
	row, ok := workers.leaseOf(box)
	if !ok || row.State != workerpool.Live {
		return nil, fmt.Errorf("search: lease %s is not live", box)
	}
	if err := h.speaksFor(box, figaroID); err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	loaded, _ := h.settings()
	pool, ok := loaded.Pool(row.Pool)
	switch {
	case !ok || !pool.Enabled:
		return nil, fmt.Errorf("search: pool %s is not enabled", row.Pool)
	case pool.SearchURL == "":
		return nil, fmt.Errorf("search: pool %s searches nothing", row.Pool)
	}
	var bearer string
	if pool.SearchCredential != "" {
		read, err := searchBearer(pool.SearchCredential)
		if err != nil {
			return nil, err
		}
		bearer = read
	}
	minute, day := pool.SearchBounds()
	if err := workers.spend(box, "search", perMinute("search_per_minute", minute), perDay("search_per_day", day)); err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{"query": query, "count": count})
	post, err := http.NewRequestWithContext(ctx, http.MethodPost, pool.SearchURL, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("search: the request could not be built")
	}
	post.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		post.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := searchClient(workers.SearchWire).Do(post)
	if err != nil {
		return nil, errors.New("search: the search endpoint was not reached")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, searchAnswerMax+1))
	switch {
	case resp.StatusCode/100 != 2:
		return nil, fmt.Errorf("search: the search endpoint answered %d", resp.StatusCode)
	case err != nil:
		return nil, errors.New("search: the answer could not be read")
	case len(raw) > searchAnswerMax:
		return nil, fmt.Errorf("search: the answer is over %d KiB", searchAnswerMax>>10)
	}
	var answer struct {
		Results []tool.SearchResult `json:"results"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil || answer.Results == nil {
		return nil, errors.New("search: the answer is not a list of results")
	}
	return answer.Results[:min(len(answer.Results), count)], nil
}

func searchBearer(credential string) (string, error) {
	path, err := boot.CredentialPath(credential)
	if err != nil {
		return "", errors.New("search: the pool's search credential has no file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("search: the pool's search credential cannot be read")
	}
	bearer := strings.TrimSpace(string(raw))
	if bearer == "" {
		return "", errors.New("search: the pool's search credential is empty")
	}
	return bearer, nil
}
