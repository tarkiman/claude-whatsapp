// Package transcribe converts WhatsApp voice notes to text using a local
// whisper.cpp build — no cloud dependency, no API cost. Benchmarked on a
// Pi 5 (4 threads): the "base" multilingual model transcribes audio at
// roughly 2.3x real-time, so even a 30s voice note finishes in well under
// gowa's message-processing window (this runs after the webhook is already
// ack'd — see internal/webhook.Handler.handleMessage — so there's no hard
// deadline anyway).
package transcribe

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Transcriber struct {
	WhisperBin string
	ModelPath  string
	FFmpegBin  string
	// Language is a whisper language code ("id", "en", ...) or "auto" to
	// let whisper detect it per clip.
	Language string
	Timeout  time.Duration
}

func New(whisperBin, modelPath, ffmpegBin, language string) *Transcriber {
	return &Transcriber{
		WhisperBin: whisperBin,
		ModelPath:  modelPath,
		FFmpegBin:  ffmpegBin,
		Language:   language,
		Timeout:    2 * time.Minute,
	}
}

// Transcribe returns the plain-text transcript of the audio file at path.
// WhatsApp voice notes arrive as Opus-in-Ogg, which whisper.cpp's bundled
// miniaudio decoder does not reliably handle, so this always normalizes
// through ffmpeg to 16kHz mono PCM WAV first regardless of the source
// container/codec.
func (t *Transcriber) Transcribe(ctx context.Context, audioPath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, t.Timeout)
	defer cancel()

	// Voice notes race docker-compose.yml's gowa-media-perms-fix sidecar,
	// which only loosens a freshly-downloaded attachment's permissions
	// (gowa writes it 0600, owned by a container-internal uid) every few
	// seconds. Images/documents rarely hit this because Claude reads them
	// several seconds into a claude -p run, but this goroutine calls
	// ffmpeg on the file almost immediately after the webhook fires — so
	// wait for it to actually become readable first instead of racing.
	if err := waitReadable(ctx, audioPath, 8*time.Second); err != nil {
		return "", err
	}

	wavPath, err := t.toWav(ctx, audioPath)
	if err != nil {
		return "", err
	}
	defer os.Remove(wavPath)

	args := []string{
		"-m", t.ModelPath,
		"-f", wavPath,
		"-l", t.Language,
		"-nt", // no timestamps
		"-np", // no logs other than the transcript itself
	}
	cmd := exec.CommandContext(ctx, t.WhisperBin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("whisper-cli: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}

	text := strings.TrimSpace(stdout.String())
	if text == "" {
		return "", fmt.Errorf("whisper-cli produced an empty transcript")
	}
	return text, nil
}

// waitReadable blocks until path can be opened for reading, or timeout
// elapses — see the comment in Transcribe for why this is needed.
func waitReadable(ctx context.Context, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		f, err := os.Open(path)
		if err == nil {
			f.Close()
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s to become readable: %w", path, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (t *Transcriber) toWav(ctx context.Context, audioPath string) (string, error) {
	out, err := os.CreateTemp("", "claude-whatsapp-voice-*.wav")
	if err != nil {
		return "", fmt.Errorf("create temp wav: %w", err)
	}
	wavPath := out.Name()
	out.Close()

	args := []string{"-y", "-i", audioPath, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wavPath}
	cmd := exec.CommandContext(ctx, t.FFmpegBin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.Remove(wavPath)
		return "", fmt.Errorf("ffmpeg convert: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return wavPath, nil
}
