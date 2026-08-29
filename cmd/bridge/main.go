// Command bridge is a small HTTP server that connects a gowa
// (go-whatsapp-web-multidevice) instance to Claude Code: inbound WhatsApp
// messages arrive as gowa webhooks, get answered by shelling out to the
// `claude` CLI (one Claude Code session per chat, resumed across messages),
// and the reply is sent back through gowa's REST API.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/tarkiman/claude-whatsapp/internal/claude"
	"github.com/tarkiman/claude-whatsapp/internal/config"
	"github.com/tarkiman/claude-whatsapp/internal/gowa"
	"github.com/tarkiman/claude-whatsapp/internal/pending"
	"github.com/tarkiman/claude-whatsapp/internal/session"
	"github.com/tarkiman/claude-whatsapp/internal/transcribe"
	"github.com/tarkiman/claude-whatsapp/internal/webhook"
)

func main() {
	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	store, err := session.Open(cfg.SessionStorePath)
	if err != nil {
		log.Fatalf("session store: %v", err)
	}

	pendingStore, err := pending.Open(cfg.PendingDir)
	if err != nil {
		log.Fatalf("pending store: %v", err)
	}

	gowaClient := gowa.New(cfg.GowaBaseURL, cfg.GowaUser, cfg.GowaPass)
	runner := claude.New(cfg.ClaudeBin, cfg.WorkDir)
	handler := webhook.New(cfg, gowaClient, runner, store, pendingStore, newTranscriber(cfg))

	replayPending(handler, pendingStore)

	mux := http.NewServeMux()
	mux.Handle("/webhook", handler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	log.Printf("claude-whatsapp bridge listening on %s (gowa=%s, workdir=%s)", cfg.ListenAddr, cfg.GowaBaseURL, cfg.WorkDir)
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, mux))
}

// newTranscriber wires up voice-note transcription if both the whisper-cli
// binary and model file are present (scripts/setup-whisper.sh installs
// them) — returns nil otherwise so voice notes fall back to the "can't
// listen" prompt instead of failing the whole bridge over an optional
// feature.
func newTranscriber(cfg *config.Config) *transcribe.Transcriber {
	if _, err := os.Stat(cfg.WhisperBin); err != nil {
		log.Printf("transcribe: disabled — whisper binary not found at %s (run scripts/setup-whisper.sh)", cfg.WhisperBin)
		return nil
	}
	if _, err := os.Stat(cfg.WhisperModel); err != nil {
		log.Printf("transcribe: disabled — whisper model not found at %s (run scripts/setup-whisper.sh)", cfg.WhisperModel)
		return nil
	}
	return transcribe.New(cfg.WhisperBin, cfg.WhisperModel, cfg.FFmpegBin, cfg.WhisperLang)
}

// replayPending re-runs any message left over from a previous process that
// died between acking gowa and finishing its reply — see internal/pending.
// Runs in the background so a large backlog can't delay the HTTP server
// (and therefore /health) from coming up.
func replayPending(handler *webhook.Handler, store *pending.Store) {
	msgs, err := store.ListAll()
	if err != nil {
		log.Printf("pending: failed to list leftover messages: %v", err)
		return
	}
	if len(msgs) == 0 {
		return
	}
	log.Printf("pending: found %d message(s) left over from a previous run, replaying", len(msgs))
	for _, m := range msgs {
		go handler.Replay(m)
	}
}
