package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// ToolUse is one tool call Claude made while working, reported as it happens.
type ToolUse struct {
	Name  string
	Input map[string]any
}

// Run is one headless turn in a project directory, streamed so the caller can
// follow what Claude is doing (see internal/jobs). It resumes resumeSessionID
// when given, falling back to a fresh session if that one is gone, like Reply.
type Run struct {
	Dir          string
	Prompt       string
	Resume       string
	AppendSystem string
	OnTool       func(ToolUse)
}

type streamEvent struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type  string         `json:"type"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	} `json:"message"`
	result
}

// Stream runs r and returns the final text and the session id to resume next time.
func (r *Runner) Stream(ctx context.Context, run Run) (text, sessionID string, err error) {
	text, sessionID, err = r.stream(ctx, run)
	if err != nil && run.Resume != "" && ctx.Err() == nil {
		run.Resume = ""
		text, sessionID, err = r.stream(ctx, run)
	}
	return text, sessionID, err
}

func (r *Runner) stream(ctx context.Context, run Run) (string, string, error) {
	args := []string{
		"-p", run.Prompt,
		"--output-format", "stream-json", "--verbose",
		"--permission-mode", "auto",
		"--setting-sources", "user,project,local",
	}
	if run.AppendSystem != "" {
		args = append(args, "--append-system-prompt", run.AppendSystem)
	}
	if run.Resume != "" {
		args = append(args, "--resume", run.Resume)
	}
	cmd := exec.CommandContext(ctx, r.Bin, args...)
	cmd.Dir = run.Dir
	// claude starts its own children (shells, test runners); cancel the whole group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", err
	}
	if err := cmd.Start(); err != nil {
		return "", "", fmt.Errorf("start claude: %w", err)
	}

	var final *result
	scan := bufio.NewScanner(stdout)
	scan.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for scan.Scan() {
		var ev streamEvent
		if json.Unmarshal(scan.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "assistant":
			for _, c := range ev.Message.Content {
				if c.Type == "tool_use" && run.OnTool != nil {
					run.OnTool(ToolUse{Name: c.Name, Input: c.Input})
				}
			}
		case "result":
			res := ev.result
			final = &res
		}
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return "", "", ctx.Err()
	}
	if final == nil {
		return "", "", fmt.Errorf("claude gave no result: %v (stderr: %s)", waitErr, strings.TrimSpace(stderr.String()))
	}
	if final.IsError {
		return "", "", fmt.Errorf("claude reported an error (stop_reason=%s): %s", final.StopReason, final.Result)
	}
	if final.SessionID == "" {
		return "", "", fmt.Errorf("claude response had no session_id")
	}
	return final.Result, final.SessionID, nil
}
