package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const nodeIDFileName = "node-id"

func ensureNodeID(root string) (string, error) {
	path := filepath.Join(root, nodeIDFileName)
	if id, err := readNodeID(path); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return id, err
	}
	tmp, err := os.CreateTemp(root, nodeIDFileName+".*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(uuid.NewString() + "\n"); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Link(tmp.Name(), path); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	return readNodeID(path)
}

func readNodeID(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(raw))
	if _, err := uuid.Parse(id); err != nil {
		return "", fmt.Errorf("store node id %s: %w", path, err)
	}
	return id, nil
}

func (s *XwalStore) NodeID() string { return s.nodeID }
