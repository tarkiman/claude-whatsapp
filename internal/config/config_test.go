package config

import "testing"

func TestIsGroupChat(t *testing.T) {
	cases := map[string]bool{
		"120363012345678901@g.us":      true,
		"6281234567890@s.whatsapp.net": false,
		"6281234567890@lid":            false,
		"":                             false,
	}
	for chatID, want := range cases {
		if got := IsGroupChat(chatID); got != want {
			t.Errorf("IsGroupChat(%q) = %v, want %v", chatID, got, want)
		}
	}
}

func TestIsAllowed(t *testing.T) {
	cfg := &Config{
		AllowedSenders: []string{"6281234567890@s.whatsapp.net"},
		AllowedGroups:  []string{"120363012345678901@g.us"},
	}

	tests := []struct {
		name   string
		chatID string
		from   string
		want   bool
	}{
		{"allowed DM", "6281234567890@s.whatsapp.net", "6281234567890@s.whatsapp.net", true},
		{"disallowed DM", "6289999999999@s.whatsapp.net", "6289999999999@s.whatsapp.net", false},
		{"allowed group, any member", "120363012345678901@g.us", "6289999999999@s.whatsapp.net", true},
		{"disallowed group", "120363099999999999@g.us", "6281234567890@s.whatsapp.net", false},
		{
			"allowed sender's own number is NOT enough to enter an unlisted group",
			"120363099999999999@g.us", "6281234567890@s.whatsapp.net", false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cfg.IsAllowed(tt.chatID, tt.from); got != tt.want {
				t.Errorf("IsAllowed(chat=%q, from=%q) = %v, want %v", tt.chatID, tt.from, got, tt.want)
			}
		})
	}
}
