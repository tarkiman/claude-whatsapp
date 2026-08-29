// Package session persists the mapping from a WhatsApp chat_id to the
// Claude Code session_id being resumed for that chat, so conversations
// survive a bridge restart.
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type Store struct {
	path string
	mu   sync.Mutex
	data map[string]string // chat_id -> claude session_id
}

func Open(path string) (*Store, error) {
	s := &Store{path: path, data: map[string]string{}}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}

	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Get(chatID string) (sessionID string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionID, ok = s.data[chatID]
	return
}

func (s *Store) Set(chatID, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[chatID] = sessionID
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
