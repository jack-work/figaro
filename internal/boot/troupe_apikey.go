package boot

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"

	hush "github.com/jack-work/hush/client"

	"github.com/jack-work/figaro/internal/apikey"
	"github.com/jack-work/figaro/internal/auth"
)

const sealedPrefix = "AGE-ENC["

func apiKeyStrategies(t Troupe) []auth.CredentialStrategy {
	var out []auth.CredentialStrategy
	if t.Auth.KeyFile != "" {
		out = append(out, &apiKeyFile{troupe: t.Name, path: t.Auth.KeyFile})
	}
	return append(out, &sealedAPIKey{troupe: t.Name, path: t.keyPath, hush: strategyHushClient})
}

func refused(troupe string) error {
	return fmt.Errorf("troupe %s refused its api key: it may be revoked or expired; mint another and run figaro troupe login %s", troupe, troupe)
}

type apiKeyFile struct {
	troupe string
	path   string
}

func (f *apiKeyFile) TryResolve() (string, bool, error) {
	raw, err := readPrivateFile(f.path)
	if err != nil {
		return "", false, fmt.Errorf("troupe %s key_file: %w", f.troupe, err)
	}
	key := strings.TrimSpace(string(raw))
	if _, err := apikey.Parse(key); err != nil {
		return "", false, fmt.Errorf("troupe %s key_file %s: %w", f.troupe, f.path, err)
	}
	return key, true, nil
}

func (f *apiKeyFile) Invalidate(string) error { return refused(f.troupe) }

func readPrivateFile(path string) ([]byte, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is mode %#o; a key file is readable by its owner alone (chmod 600)", path, st.Mode().Perm())
	}
	if uid, ok := fileOwner(st); ok && uid != os.Getuid() {
		return nil, fmt.Errorf("%s belongs to uid %d, not this process (%d)", path, uid, os.Getuid())
	}
	buf := make([]byte, 4096)
	n, err := fh.Read(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

type sealedAPIKey struct {
	troupe string
	path   string
	hush   func() (*hush.Client, error)

	mu     sync.Mutex
	cipher string
	plain  string
}

func (s *sealedAPIKey) TryResolve() (string, bool, error) {
	raw, err := readPrivateFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, fmt.Errorf("troupe %s: no api key stored; figaro troupe login %s", s.troupe, s.troupe)
	}
	if err != nil {
		return "", false, err
	}
	cipher := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(cipher, sealedPrefix) {
		return "", false, fmt.Errorf("%s holds no sealed key; figaro troupe login %s", s.path, s.troupe)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cipher == cipher {
		return s.plain, true, nil
	}
	c, err := s.hush()
	if err != nil {
		return "", false, err
	}
	res, err := c.Decrypt(map[string]string{"v": cipher})
	if err != nil {
		return "", false, fmt.Errorf("hush decrypt %s: %w", s.path, err)
	}
	s.cipher, s.plain = cipher, res["v"]
	return s.plain, true, nil
}

func (s *sealedAPIKey) Invalidate(string) error { return refused(s.troupe) }

func SealAPIKey(c *hush.Client, t Troupe, key string) error {
	out, err := c.Encrypt(map[string]string{"v": key})
	if err != nil {
		return fmt.Errorf("hush encrypt: %w", err)
	}
	sealed := out["v"]
	if !strings.HasPrefix(sealed, sealedPrefix) {
		return errors.New("hush returned no ciphertext")
	}
	tmp := t.keyPath + ".tmp"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(sealed + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, t.keyPath)
}
