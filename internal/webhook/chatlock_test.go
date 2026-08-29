package webhook

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestChatLocksSerializesSameChat reproduces the bug seen live 2026-08-29:
// two messages for the same chat_id, dispatched at the same instant, must
// never run their critical section concurrently.
func TestChatLocksSerializesSameChat(t *testing.T) {
	locks := newChatLocks()
	var active int32
	var maxObservedConcurrency int32
	var wg sync.WaitGroup

	const n = 20
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			unlock := locks.Lock("same-chat")
			defer unlock()

			cur := atomic.AddInt32(&active, 1)
			for {
				max := atomic.LoadInt32(&maxObservedConcurrency)
				if cur <= max || atomic.CompareAndSwapInt32(&maxObservedConcurrency, max, cur) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond) // simulate work, wide enough to catch a race
			atomic.AddInt32(&active, -1)
		}()
	}
	wg.Wait()

	if maxObservedConcurrency != 1 {
		t.Fatalf("expected exactly 1 concurrent holder for the same chat_id, observed %d", maxObservedConcurrency)
	}
}

// TestChatLocksAllowsDifferentChatsConcurrently makes sure the fix doesn't
// over-serialize — unrelated chats must not block each other.
func TestChatLocksAllowsDifferentChatsConcurrently(t *testing.T) {
	locks := newChatLocks()
	var wg sync.WaitGroup
	start := make(chan struct{})
	release := make(chan struct{})
	entered := make(chan string, 2)

	for _, chat := range []string{"chat-a", "chat-b"} {
		wg.Add(1)
		go func(chatID string) {
			defer wg.Done()
			<-start
			unlock := locks.Lock(chatID)
			defer unlock()
			entered <- chatID
			<-release
		}(chat)
	}

	close(start)

	// Both should be able to enter without waiting on each other.
	timeout := time.After(1 * time.Second)
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case c := <-entered:
			seen[c] = true
		case <-timeout:
			t.Fatalf("different chat_ids blocked each other — only saw %v enter within 1s", seen)
		}
	}

	close(release)
	wg.Wait()
}
