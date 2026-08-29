package webhook

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tarkiman/claude-whatsapp/internal/config"
)

// mediaRef parses gowa's polymorphic attachment fields: a bare string when
// WHATSAPP_AUTO_DOWNLOAD_MEDIA is on and there's no caption (the default —
// see docs/ARCHITECTURE.md), or an object with path/url/caption/filename
// otherwise. See gowa's docs/webhook-payload.md "Media Messages" section.
type mediaRef struct {
	Path     string
	URL      string
	Caption  string
	Filename string
}

func (m *mediaRef) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		m.Path = s
		return nil
	}
	var obj struct {
		Path     string `json:"path"`
		URL      string `json:"url"`
		Caption  string `json:"caption"`
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	m.Path, m.URL, m.Caption, m.Filename = obj.Path, obj.URL, obj.Caption, obj.Filename
	return nil
}

// attachment picks the one attachment on a message, if any — WhatsApp
// messages carry at most one media item, so the first non-nil field wins.
func attachment(p *payload) (mediaType string, ref *mediaRef) {
	switch {
	case p.Image != nil:
		return "image", p.Image
	case p.Video != nil:
		return "video", p.Video
	case p.Document != nil:
		return "document", p.Document
	case p.Sticker != nil:
		return "sticker", p.Sticker
	case p.Audio != nil:
		return "audio", p.Audio
	default:
		return "", nil
	}
}

// resolveMediaPath turns a webhook-reported path like "statics/media/x.jpg"
// into the absolute host path claude's Read tool can open directly — see
// the docker-compose.yml volume mount ("./data/statics:/app/statics") this
// depends on.
func resolveMediaPath(cfg *config.Config, ref *mediaRef) string {
	if ref == nil || ref.Path == "" {
		return ""
	}
	rel := strings.TrimPrefix(ref.Path, "statics/")
	abs, err := filepath.Abs(filepath.Join(cfg.MediaDir, rel))
	if err != nil {
		return filepath.Join(cfg.MediaDir, rel)
	}
	return abs
}

// buildPrompt combines the message body/caption with an explicit
// instruction about any attachment, so claude -p gets one self-contained
// text prompt (there's no separate multimodal-attachment API in print
// mode) — it reads the file itself via its own Read tool.
func buildPrompt(body, mediaType, mediaPath string, ref *mediaRef) string {
	caption := body
	if caption == "" && ref != nil {
		caption = ref.Caption
	}

	// Voice notes get their attachment note built later, by
	// FinalizeAudioPrompt after a transcription attempt — that can take
	// longer than the ~10s gowa allows this webhook handler to ack, so it
	// runs in handler.go's async goroutine instead. buildPrompt only
	// contributes the caption here; the pending.Message.Prompt persisted
	// from this return value is the caption alone for audio.
	if mediaType == "audio" {
		return caption
	}

	var b strings.Builder
	if caption != "" {
		b.WriteString(caption)
	}
	if mediaType == "" {
		return b.String()
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}

	switch mediaType {
	case "image":
		if mediaPath != "" {
			fmt.Fprintf(&b, "[Lampiran gambar: %s — baca file ini untuk melihat isinya sebelum membalas]", mediaPath)
		} else {
			b.WriteString("[Lampiran gambar, tapi file lokalnya tidak tersedia — beri tahu pengirim.]")
		}
	case "video":
		if mediaPath != "" {
			fmt.Fprintf(&b, "[Lampiran video: %s — file ada di disk, tapi belum bisa ditonton otomatis; beri tahu pengirim kalau perlu dijelaskan isinya secara manual]", mediaPath)
		} else {
			b.WriteString("[Lampiran video, tapi file lokalnya tidak tersedia — beri tahu pengirim.]")
		}
	case "document":
		if mediaPath != "" {
			fmt.Fprintf(&b, "[Lampiran dokumen: %s — baca file ini (PDF/teks bisa langsung dibaca) sebelum membalas]", mediaPath)
		} else {
			b.WriteString("[Lampiran dokumen, tapi file lokalnya tidak tersedia — beri tahu pengirim.]")
		}
	case "sticker":
		if mediaPath != "" {
			fmt.Fprintf(&b, "[Lampiran stiker: %s — coba baca file ini untuk lihat isinya]", mediaPath)
		} else {
			b.WriteString("[Lampiran stiker, tapi file lokalnya tidak tersedia.]")
		}
	}
	return b.String()
}

// FinalizeAudioPrompt builds the prompt for a voice note after a
// transcription attempt (success or failure) — called from handler.go's
// async goroutine, not buildPrompt, since transcription can take longer
// than the ~10s gowa gives the webhook handler to ack. caption is
// buildPrompt's earlier return value for this message (the raw
// body/caption text, persisted as pending.Message.Prompt).
func FinalizeAudioPrompt(caption, mediaPath, transcript string, transcribeErr error) string {
	var b strings.Builder
	if caption != "" {
		b.WriteString(caption)
		b.WriteString("\n\n")
	}
	switch {
	case transcribeErr == nil && transcript != "":
		fmt.Fprintf(&b, "[Transkrip voice note]: %s", transcript)
	case mediaPath != "":
		fmt.Fprintf(&b, "[Lampiran voice note: %s — transkripsi otomatis gagal kali ini, minta pengirim ketik ulang isinya kalau penting.]", mediaPath)
	default:
		b.WriteString("[Lampiran voice note, tapi file lokalnya tidak tersedia — minta pengirim ketik ulang isinya.]")
	}
	return b.String()
}
