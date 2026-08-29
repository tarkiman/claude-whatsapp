// Package claude shells out to the `claude` CLI in headless print mode
// (`-p`) to answer one WhatsApp message at a time, resuming the same Claude
// Code session across messages in a chat so context carries over — without
// needing a long-lived interactive process.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Runner struct {
	Bin     string
	WorkDir string
	Timeout time.Duration
}

func New(bin, workDir string) *Runner {
	return &Runner{Bin: bin, WorkDir: workDir, Timeout: 10 * time.Minute}
}

type result struct {
	Type       string `json:"type"`
	IsError    bool   `json:"is_error"`
	Result     string `json:"result"`
	SessionID  string `json:"session_id"`
	StopReason string `json:"stop_reason"`
}

// Reply runs one turn of the conversation for chatSessionID (empty for a
// brand new conversation) and returns the response text plus the session_id
// to remember for next time.
func (r *Runner) Reply(ctx context.Context, prompt, resumeSessionID string) (text, newSessionID string, err error) {
	text, newSessionID, err = r.run(ctx, prompt, resumeSessionID)
	if err != nil && resumeSessionID != "" {
		// The remembered session may no longer exist (cleared, expired, or
		// this is a fresh install) — retry once as a brand new conversation
		// rather than leaving the chat unanswered.
		text, newSessionID, err = r.run(ctx, prompt, "")
	}
	return text, newSessionID, err
}

func (r *Runner) run(ctx context.Context, prompt, resumeSessionID string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--permission-mode", "auto",
		"--setting-sources", "user,project,local",
	}
	if resumeSessionID != "" {
		args = append(args, "--resume", resumeSessionID)
	}

	cmd := exec.CommandContext(ctx, r.Bin, args...)
	cmd.Dir = r.WorkDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("claude exited: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}

	var res result
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return "", "", fmt.Errorf("parse claude output: %w (raw: %s)", err, stdout.String())
	}
	if res.IsError {
		return "", "", fmt.Errorf("claude reported an error (stop_reason=%s): %s", res.StopReason, res.Result)
	}
	if res.SessionID == "" {
		return "", "", fmt.Errorf("claude response had no session_id")
	}

	return res.Result, res.SessionID, nil
}
