package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/jack-work/figaro/internal/config"
)

func persistLoginHeaders(loaded *config.Loaded, name string, headers map[string]string) error {
	if len(headers) == 0 {
		return nil
	}
	refuse := errors.New("oauth: could not save login account metadata; login is not complete")
	path := loaded.ProviderAuthPath(name)
	data, err := os.ReadFile(path)
	if err != nil {
		return refuse
	}
	var doc map[string]any
	if _, err = toml.Decode(string(data), &doc); err != nil {
		return refuse
	}
	table, ok := doc["headers"].(map[string]any)
	if !ok {
		if _, exists := doc["headers"]; exists {
			return refuse
		}
		table = map[string]any{}
	}
	for k, v := range headers {
		for existing := range table {
			if strings.EqualFold(existing, k) {
				delete(table, existing)
			}
		}
		table[k] = v
	}
	doc["headers"] = table
	var encoded bytes.Buffer
	if toml.NewEncoder(&encoded).Encode(doc) != nil {
		return refuse
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".login-metadata-*")
	if err != nil {
		return refuse
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(encoded.Bytes())
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return refuse
	}
	if os.Rename(temporary, path) != nil {
		return refuse
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return refuse
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return refuse
	}
	return nil
}
