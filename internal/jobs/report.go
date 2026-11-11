package jobs

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tarkiman/claude-whatsapp/internal/claude"
)

// SystemPrompt is appended to Claude Code's own for every voice job. The
// instruction arrives through speech recognition, the summary is read aloud by
// Eca (who speaks as herself and never names the tool), and outward or
// destructive actions wait for a spoken confirmation.
const SystemPrompt = `This task was given by voice. Tarkiman ("Abi") spoke it to his assistant Eca, and speech recognition turned it into text. You are doing the work on Eca's behalf.

Rules:
1. Speech recognition mishears names: read file names, identifiers and commands in the instruction generously and match them to what exists in this repository. If the request is truly ambiguous or risky to guess, do the safe part and ask.
2. Do not push, merge, open or close pull requests, deploy, release, publish, delete branches, delete files outside the task, or run anything destructive or outward-facing UNLESS the instruction contains "KONFIRMASI: ya" for that action. Without it, finish the safe part and ask for confirmation in RINGKASAN.
3. Follow this repository's CLAUDE.md and its project memory, as in any session.
4. Keep working until the task is done or you need Abi's decision; nobody can answer you mid-task.

End your final message with exactly these two sections:
RINGKASAN:
<one to three short sentences in spoken Indonesian, said by Eca to Abi in the first person ("Eca", "aku"). No code, file paths, markdown, URLs or numbers that are hard to hear. Never mention Claude, Claude Code or an AI. If you need a decision or confirmation, end with that question.>
DETAIL:
<the full report for WhatsApp, in Indonesian: what changed (files), tests run and their results, commits, branch, PR links, and any open question.>`

var (
	summaryRe = regexp.MustCompile(`(?is)RINGKASAN:\s*(.*?)\s*(?:DETAIL:\s*(.*))?$`)
	wordsRe   = regexp.MustCompile(`\s+`)
)

// ParseReport splits the final message into the spoken summary and the WhatsApp detail.
// Without the sections, the first few sentences become the summary and the whole text the detail.
func ParseReport(text string) (summary, detail string) {
	text = strings.TrimSpace(text)
	if loc := summaryRe.FindStringSubmatchIndex(text); loc != nil {
		summary = clean(text[loc[2]:loc[3]])
		if loc[4] >= 0 {
			detail = strings.TrimSpace(text[loc[4]:loc[5]])
		}
		if before := strings.TrimSpace(text[:loc[0]]); detail == "" {
			detail = before
		}
		return summary, detail
	}
	return clean(firstSentences(text, 300)), text
}

// clean makes text speakable: no markdown, links or code.
func clean(s string) string {
	s = regexp.MustCompile("`[^`]*`").ReplaceAllString(s, "")
	s = regexp.MustCompile(`https?://\S+`).ReplaceAllString(s, "")
	s = strings.NewReplacer("**", "", "__", "", "#", "", "*", "", "_", " ").Replace(s)
	return strings.TrimSpace(wordsRe.ReplaceAllString(s, " "))
}

func firstSentences(s string, max int) string {
	s = wordsRe.ReplaceAllString(s, " ")
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndexAny(cut, ".!?"); i > max/3 {
		return cut[:i+1]
	}
	return cut + "…"
}

// Describe says, in spoken Indonesian, what a tool call is doing ("sedang menjalankan tes").
func Describe(t claude.ToolUse) string {
	str := func(k string) string { v, _ := t.Input[k].(string); return v }
	file := filepath.Base(str("file_path"))
	switch t.Name {
	case "Read":
		return "membaca " + file
	case "Edit", "MultiEdit":
		return "mengubah " + file
	case "Write":
		return "menulis " + file
	case "Grep", "Glob":
		return "mencari di kode"
	case "WebSearch", "WebFetch":
		return "mencari di internet"
	case "Task", "Agent":
		return "membagi pekerjaan"
	case "TodoWrite":
		return "menyusun langkah kerja"
	case "Bash":
		return describeCommand(str("command"))
	}
	return "bekerja (" + t.Name + ")"
}

func describeCommand(cmd string) string {
	c := strings.ToLower(cmd)
	switch {
	case containsAny(c, "go test", "pytest", "npm test", "npm run test", "vitest", "gradlew test", "cargo test"):
		return "menjalankan tes"
	case containsAny(c, "git push"):
		return "mengirim ke GitHub"
	case containsAny(c, "git commit"):
		return "membuat commit"
	case containsAny(c, "gh pr"):
		return "mengurus pull request"
	case strings.HasPrefix(c, "git ") || strings.Contains(c, " git "):
		return "memeriksa git"
	case containsAny(c, "go build", "npm run build", "gradlew", "docker build", "docker compose", "make "):
		return "membangun aplikasi"
	case containsAny(c, "npm install", "go mod", "pip install"):
		return "memasang dependensi"
	case containsAny(c, "curl ", "wget "):
		return "mengecek layanan"
	}
	return fmt.Sprintf("menjalankan perintah %s", firstWord(cmd))
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func firstWord(cmd string) string {
	f := strings.Fields(cmd)
	if len(f) == 0 {
		return ""
	}
	return filepath.Base(f[0])
}
