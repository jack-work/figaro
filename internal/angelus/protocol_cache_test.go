package angelus

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jack-work/figaro/internal/rpc"
)

func TestListChalkboardCache(t *testing.T) {
	h := &handlers{}
	h.cacheListChalkboard("aria", listChalkboardEntry{
		provider:    "copilot",
		model:       "gpt-5.6-terra",
		mantra:      "work",
		cwd:         "C:\\work",
		loadoutName: "default",
		loadoutVer:  "live",
		at:          time.Now(),
	})

	entry := rpc.FigaroInfoResponse{ID: "aria", State: "dormant"}
	cached, ok := h.cachedListChalkboard("aria")
	assert.True(t, ok)
	cached.apply(&entry)
	assert.Equal(t, "copilot", entry.Provider)
	assert.Equal(t, "gpt-5.6-terra", entry.Model)
	assert.Equal(t, "work", entry.Mantra)
	assert.Equal(t, "C:\\work", entry.Cwd)
	assert.Equal(t, "default", entry.LoadoutName)
	assert.Equal(t, "live", entry.LoadoutVer)

	h.cacheListChalkboard("expired", listChalkboardEntry{at: time.Now().Add(-16 * time.Second)})
	_, ok = h.cachedListChalkboard("expired")
	assert.False(t, ok)
}
