// Package session provides append-only JSONL conversation persistence.
//
// Each session is a tree of entries (id + parentId). The session tracks
// a leaf pointer — the current tip. Appending creates a child of the
// leaf. Context reconstruction walks from leaf to root, collecting
// messages along the path. This supports future forking without
// modifying history.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jack-work/figaro/internal/message"
)

type EntryType string

const (
	EntryHeader  EntryType = "header"
	EntryMessage EntryType = "message"
)

type Entry struct {
	Type      EntryType        `json:"type"`
	ID        string           `json:"id"`
	ParentID  string           `json:"parent_id,omitempty"`
	Timestamp string           `json:"timestamp"`
	Message   *message.Message `json:"message,omitempty"`
	Version   int              `json:"version,omitempty"`
	Cwd       string           `json:"cwd,omitempty"`
}

type Session struct {
	file    string
	entries []Entry
	byID    map[string]*Entry
	leafID  string
	flushed bool
	nextSeq int
}

func New(path, cwd string) (*Session, error) {
	s := &Session{file: path, byID: make(map[string]*Entry)}
	header := Entry{
		Type: EntryHeader, ID: s.genID(),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Version: 1, Cwd: cwd,
	}
	s.entries = append(s.entries, header)
	return s, nil
}

func Load(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read session: %w", err)
	}
	s := &Session{file: path, byID: make(map[string]*Entry), flushed: true}
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		s.entries = append(s.entries, entry)
		if entry.Type != EntryHeader {
			s.byID[entry.ID] = &s.entries[len(s.entries)-1]
			s.leafID = entry.ID
		}
	}
	return s, nil
}

func (s *Session) Append(msg message.Message) string {
	id := s.genID()
	entry := Entry{
		Type: EntryMessage, ID: id, ParentID: s.leafID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Message:   &msg,
	}
	s.entries = append(s.entries, entry)
	s.byID[id] = &s.entries[len(s.entries)-1]
	s.leafID = id
	s.persist(entry)
	return id
}

func (s *Session) BuildContext() []message.Message {
	if s.leafID == "" {
		return nil
	}
	var path []string
	current := s.leafID
	for current != "" {
		path = append(path, current)
		if e, ok := s.byID[current]; ok {
			current = e.ParentID
		} else {
			break
		}
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	var msgs []message.Message
	for _, id := range path {
		if e, ok := s.byID[id]; ok && e.Message != nil {
			msgs = append(msgs, *e.Message)
		}
	}
	return msgs
}

func (s *Session) LeafID() string { return s.leafID }
func (s *Session) Path() string   { return s.file }

func (s *Session) persist(entry Entry) {
	if s.file == "" {
		return
	}
	hasAssistant := false
	for _, e := range s.entries {
		if e.Type == EntryMessage && e.Message != nil && e.Message.Role == message.RoleAssistant {
			hasAssistant = true
			break
		}
	}
	if !hasAssistant {
		s.flushed = false
		return
	}
	if !s.flushed {
		if err := os.MkdirAll(filepath.Dir(s.file), 0o755); err != nil {
			return
		}
		f, err := os.Create(s.file)
		if err != nil {
			return
		}
		for _, e := range s.entries {
			data, _ := json.Marshal(e)
			f.Write(data)
			f.Write([]byte("\n"))
		}
		f.Close()
		s.flushed = true
	} else {
		f, err := os.OpenFile(s.file, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		data, _ := json.Marshal(entry)
		f.Write(data)
		f.Write([]byte("\n"))
		f.Close()
	}
}

func (s *Session) genID() string {
	s.nextSeq++
	return fmt.Sprintf("%08x", s.nextSeq)
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
