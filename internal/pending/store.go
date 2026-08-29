// Package pending is a durable, on-disk queue of inbound WhatsApp messages
// that have been ack'd to gowa but not yet fully answered. It exists to
// close one gap: the webhook handler must ack gowa within ~10s, so the
// actual claude/reply work happens in a background goroutine — if the
// bridge process dies between the ack and finishing that work, gowa never
// retries (it already got its 200 OK) and the message would be lost with
// no trace. Writing a pending file before the ack, and deleting it only
// once a reply (success or the error-fallback message) has actually been
// sent, means a crash leaves evidence on disk that main.go can replay on
// the next startup.
package pending

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

type Message struct {
	MessageID string `json:"message_id"`
	ChatID    string `json:"chat_id"`
	From      string `json:"from"`
	// Prompt is the exact text sent to `claude -p` — plain message body for
	// a text message, or body/caption plus an attachment instruction
	// pointing at the resolved local file path (see internal/webhook/media.go).
	Prompt     string    `json:"prompt"`
	ReceivedAt time.Time `json:"received_at"`

	// Set when the message carries an attachment. MediaPath is an absolute
	// host filesystem path (resolved from gowa's webhook payload) that
	// claude's Read tool can open directly. MediaType is one of "image",
	// "video", "document", "audio", "sticker".
	MediaPath string `json:"media_path,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

type Store struct {
	dir string
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create pending dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

var unsafeFilenameChars = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

func (s *Store) path(messageID string) string {
	safe := unsafeFilenameChars.ReplaceAllString(messageID, "_")
	return filepath.Join(s.dir, safe+".json")
}

// Write persists a message that has been ack'd to gowa but not yet
// answered. Naming the file by message_id makes this idempotent — a
// redelivered webhook for the same message just overwrites the same file
// rather than queuing a duplicate.
func (s *Store) Write(m Message) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(m.MessageID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(m.MessageID))
}

// Done removes a message once it has actually been answered (successfully
// or with the error-fallback reply) — either way the sender got a
// response, so there's nothing left to recover.
func (s *Store) Done(messageID string) error {
	err := os.Remove(s.path(messageID))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ListAll returns every message still on disk — i.e. every message that
// was ack'd to gowa but never got a reply, most likely because the bridge
// was killed mid-flight. Called once at startup to replay them.
func (s *Store) ListAll() ([]Message, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}

	var out []Message
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue // best-effort — a half-written file shouldn't block the rest
		}
		var m Message
		if err := json.Unmarshal(b, &m); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}
