package config

import "testing"

func TestMatchesAllowlist(t *testing.T) {
	cases := []struct {
		name  string
		list  []string
		jid   string
		allow bool
	}{
		{"full JID, exact", []string{"6281234567890@s.whatsapp.net"}, "6281234567890@s.whatsapp.net", true},
		{"bare number, exact", []string{"6281234567890"}, "6281234567890@s.whatsapp.net", true},
		{"same person, other device", []string{"6281234567890@s.whatsapp.net"}, "6281234567890:12@s.whatsapp.net", true},
		{"bare entry is not a prefix wildcard", []string{"628123456789"}, "6281234567890@s.whatsapp.net", false},
		{"longer entry does not match shorter number", []string{"6281234567890"}, "628123456789@s.whatsapp.net", false},
		{"bare number never matches an @lid", []string{"6281234567890"}, "6281234567890@lid", false},
		{"full JID never matches another server", []string{"6281234567890@s.whatsapp.net"}, "6281234567890@lid", false},
		{"group JID, exact", []string{"120363012345678901@g.us"}, "120363012345678901@g.us", true},
		{"group JID, different group", []string{"120363012345678901@g.us"}, "120363099999999999@g.us", false},
		{"bare number never matches a group", []string{"120363012345678901"}, "120363012345678901@g.us", false},
		{"empty list allows nobody", nil, "6281234567890@s.whatsapp.net", false},
		{"empty jid allows nobody", []string{"6281234567890"}, "", false},
		{"empty entry allows nobody", []string{""}, "6281234567890@s.whatsapp.net", false},
		{"one of several", []string{"6280000000000", "6281234567890"}, "6281234567890@s.whatsapp.net", true},
	}
	for _, c := range cases {
		if got := matchesAllowlist(c.list, c.jid); got != c.allow {
			t.Errorf("%s: matchesAllowlist(%q, %q) = %v, want %v", c.name, c.list, c.jid, got, c.allow)
		}
	}
}
