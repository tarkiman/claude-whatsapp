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

	"github.com/tarkiman/claude-whatsapp/internal/access"
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

	accessStore := newAccessStore(cfg)

	gowaClient := gowa.New(cfg.GowaBaseURL, cfg.GowaUser, cfg.GowaPass)
	runner := claude.New(cfg.ClaudeBin, cfg.WorkDir)
	handler := webhook.New(cfg, gowaClient, runner, store, pendingStore, newTranscriber(cfg), accessStore)

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

// newAccessStore builds the access policy: the access file written by the
// Admin UI when it exists, otherwise personal mode for the number in
// ALLOWED_SENDERS. It logs what is in force, and warns about settings from
// older versions that no longer grant anything.
func newAccessStore(cfg *config.Config) *access.Store {
	legacy, ignored := access.Legacy(cfg.AllowedSenders)
	if len(ignored) > 0 {
		log.Printf("access: personal mode allows exactly one number — ignoring extra/invalid ALLOWED_SENDERS entries: %v", ignored)
	}
	if len(cfg.AllowedGroups) > 0 {
		log.Printf("access: ALLOWED_GROUPS is deprecated and admits nobody any more — set up team mode (group + approved members) in the Admin UI")
	}
	st := access.Open(cfg.AccessFile, legacy)
	pol := st.Get()
	switch {
	case st.Err() != nil:
		log.Printf("access: %v — denying everyone until it is fixed", st.Err())
	case pol.Mode == access.ModeTeam:
		log.Printf("access: team mode (source=%s) group=%s, %d approved member(s)", st.Source(), pol.Team.Group, len(pol.Team.Members))
	default:
		log.Printf("access: personal mode (source=%s), groups ignored", st.Source())
	}
	return st
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
