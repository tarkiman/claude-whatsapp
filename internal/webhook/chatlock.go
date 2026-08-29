package webhook

import "sync"

// chatLocks serializes message handling per chat_id. Claude Code sessions
// aren't designed for concurrent `--resume` access from multiple processes
// — two messages for the same chat arriving close together (or replayed
// together after a restart, see internal/pending) will otherwise both
// spawn `claude -p --resume <same id>` at once. Observed live 2026-08-29:
// a bridge restart mid-flight replayed two pending messages for one chat
// simultaneously, and the reply that should've taken seconds instead sat
// for minutes with nothing happening until the *next* restart tore both
// processes down. This forces one `claude -p` per chat_id at a time;
// anything else for the same chat just waits its turn.
type chatLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newChatLocks() *chatLocks {
	return &chatLocks{locks: make(map[string]*sync.Mutex)}
}

// Lock blocks until it's this chat_id's turn, and returns a func to call
// (typically via defer) once the caller is done.
func (c *chatLocks) Lock(chatID string) (unlock func()) {
	c.mu.Lock()
	l, ok := c.locks[chatID]
	if !ok {
		l = &sync.Mutex{}
		c.locks[chatID] = l
	}
	c.mu.Unlock()

	l.Lock()
	return l.Unlock
}
