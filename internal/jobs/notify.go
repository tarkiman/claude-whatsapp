package jobs

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// Notifier posts {"text", "source"} to a URL: kawan-bridge's local /notify, which
// has Eca read it out on whichever device Abi is using.
func Notifier(url string) func(string) {
	if url == "" {
		return nil
	}
	client := &http.Client{Timeout: 30 * time.Second}
	return func(text string) {
		body, _ := json.Marshal(map[string]string{"text": text, "source": "kode"})
		resp, err := client.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("jobs: notify: %v", err)
			return
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			log.Printf("jobs: notify: HTTP %d", resp.StatusCode)
		}
	}
}
