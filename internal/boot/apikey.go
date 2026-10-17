package boot

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"

	hush "github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/config"
)

type providerAuthFile struct {
	loaded *config.Loaded
	name   string

	mu   sync.Mutex
	have bool
	mod  time.Time
	size int64
	pa   config.ProviderAuth
}

func (f *providerAuthFile) path() string { return f.loaded.ProviderAuthPath(f.name) }

func (f *providerAuthFile) load() (config.ProviderAuth, error) {
	st, err := os.Stat(f.path())
	if errors.Is(err, fs.ErrNotExist) {
		return config.ProviderAuth{}, nil
	}
	if err != nil {
		return config.ProviderAuth{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.have && st.ModTime().Equal(f.mod) && st.Size() == f.size {
		return f.pa, nil
	}
	var pa config.ProviderAuth
	if err := f.loaded.LoadProviderAuth(f.name, &pa); err != nil {
		return config.ProviderAuth{}, err
	}
	f.pa, f.mod, f.size, f.have = pa, st.ModTime(), st.Size(), true
	return pa, nil
}

type encryptedAPIKey struct {
	Hush func() (*hush.Client, error)
	File *providerAuthFile

	mu     sync.Mutex
	cipher string
	plain  string
}

var _ auth.CredentialStrategy = (*encryptedAPIKey)(nil)

func (e *encryptedAPIKey) TryResolve() (string, bool, error) {
	pa, err := e.File.load()
	if err != nil || !strings.HasPrefix(pa.APIKey, "AGE-ENC[") {
		return "", false, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cipher == pa.APIKey {
		return e.plain, true, nil
	}
	c, err := e.Hush()
	if err != nil {
		return "", false, err
	}
	res, err := c.Decrypt(map[string]string{"v": pa.APIKey})
	if err != nil {
		return "", false, fmt.Errorf("hush decrypt %s: %w", e.File.path(), err)
	}
	e.cipher, e.plain = pa.APIKey, res["v"]
	return e.plain, true, nil
}

func (e *encryptedAPIKey) Invalidate(token string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.plain == token {
		e.cipher, e.plain = "", ""
	}
	return nil
}
