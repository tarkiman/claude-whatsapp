package access

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	owner   = "6281234567890"
	ownerJ  = owner + "@s.whatsapp.net"
	group   = "120363012345678901@g.us"
	botNum  = "6289500000001"
	botLID  = "100000000000009"
	alice   = "6281200000002"
	aliceJ  = alice + "@s.whatsapp.net"
	aliceL  = "100000000000002"
	mallory = "6289999999999@s.whatsapp.net"
)

var bot = Bot{Phone: botNum, LID: botLID}

func team(members ...Member) Config {
	return Config{Mode: ModeTeam, Team: Team{Group: group, Members: members}}
}

func personal() Config { return Config{Mode: ModePersonal, Personal: Personal{Number: owner}} }

func TestPersonalMode(t *testing.T) {
	c := personal()
	cases := []struct {
		name  string
		in    Incoming
		allow bool
	}{
		{"the owner's DM", Incoming{ChatID: ownerJ, From: ownerJ, Body: "hi"}, true},
		{"owner's other device", Incoming{ChatID: ownerJ, From: owner + ":12@s.whatsapp.net", Body: "hi"}, true},
		{"a stranger's DM", Incoming{ChatID: mallory, From: mallory, Body: "hi"}, false},
		{"owner writing in a group", Incoming{ChatID: group, From: ownerJ, Body: "@" + botNum + " hi"}, false},
		{"anyone mentioning the bot in a group", Incoming{ChatID: group, From: mallory, Body: "@" + botNum + " hi"}, false},
		{"shorter number sharing a prefix", Incoming{ChatID: "628123456789@s.whatsapp.net", From: "628123456789@s.whatsapp.net"}, false},
		{"@lid with the owner's digits", Incoming{ChatID: owner + "@lid", From: owner + "@lid"}, false},
	}
	for _, tc := range cases {
		if got := c.Decide(tc.in, bot).Allow; got != tc.allow {
			t.Errorf("%s: allow=%v, want %v", tc.name, got, tc.allow)
		}
	}
}

func TestPersonalModeWithNoNumberAllowsNobody(t *testing.T) {
	c := Config{Mode: ModePersonal}
	if c.Decide(Incoming{ChatID: ownerJ, From: ownerJ, Body: "hi"}, bot).Allow {
		t.Fatal("personal mode without a number must allow nobody")
	}
	if (Config{}).Decide(Incoming{ChatID: ownerJ, From: ownerJ, Body: "hi"}, bot).Allow {
		t.Fatal("an empty policy must allow nobody")
	}
}

func TestTeamModeRequiresApprovedMemberAndMention(t *testing.T) {
	c := team(Member{Phone: alice, LID: aliceL, Name: "Alice"})
	mention := "@" + botNum + " progress terakhir sampai mana?"
	cases := []struct {
		name   string
		in     Incoming
		allow  bool
		reason string
	}{
		{"approved member mentions the bot", Incoming{ChatID: group, From: aliceJ, Body: mention}, true, ""},
		{"mention written with the bot's LID", Incoming{ChatID: group, From: aliceJ, Body: "@" + botLID + " status?"}, true, ""},
		{"approved member, sender only known by LID", Incoming{ChatID: group, From: aliceL + "@lid", Body: mention}, true, ""},
		{"approved member, LID in from_lid", Incoming{ChatID: group, From: "", FromLID: aliceL + "@lid", Body: mention}, true, ""},
		{"stranger known only by a different LID", Incoming{ChatID: group, From: "111111111111111@lid", Body: mention}, false, "team:not_approved"},
		{"stranger whose from_lid is a different LID", Incoming{ChatID: group, From: mallory, FromLID: "111111111111111@lid", Body: mention}, false, "team:not_approved"},
		{"approved member, no mention", Incoming{ChatID: group, From: aliceJ, Body: "morning all"}, false, "team:not_mentioned"},
		{"non-member mentions the bot", Incoming{ChatID: group, From: mallory, Body: mention}, false, "team:not_approved"},
		{"non-member without mention", Incoming{ChatID: group, From: mallory, Body: "hi"}, false, "team:not_mentioned"},
		{"approved member in a different group", Incoming{ChatID: "120363999999999999@g.us", From: aliceJ, Body: mention}, false, "team:other_group"},
		{"approved member in a DM", Incoming{ChatID: aliceJ, From: aliceJ, Body: mention}, false, "team:dm_ignored"},
		{"mention of somebody else", Incoming{ChatID: group, From: aliceJ, Body: "@" + alice + " look at this"}, false, "team:not_mentioned"},
		{"mention that only shares a prefix with the bot", Incoming{ChatID: group, From: aliceJ, Body: "@" + botNum[:len(botNum)-1] + " hi"}, false, "team:not_mentioned"},
		{"mention that extends the bot's number", Incoming{ChatID: group, From: aliceJ, Body: "@" + botNum + "1 hi"}, false, "team:not_mentioned"},
		{"bare mention with nothing to do", Incoming{ChatID: group, From: aliceJ, Body: "@" + botNum}, false, "team:empty"},
		{"bare mention with an attachment", Incoming{ChatID: group, From: aliceJ, Body: "@" + botNum, HasMedia: true}, true, ""},
	}
	for _, tc := range cases {
		d := c.Decide(tc.in, bot)
		if d.Allow != tc.allow {
			t.Errorf("%s: allow=%v (%s), want %v", tc.name, d.Allow, d.Reason, tc.allow)
		}
		if !tc.allow && d.Reason != tc.reason {
			t.Errorf("%s: reason=%q, want %q", tc.name, d.Reason, tc.reason)
		}
	}
}

func TestNewGroupMembersAreDeniedByDefault(t *testing.T) {
	// The roster lists only APPROVED members: someone added to the group later
	// is not on it, so mentioning the bot gets them nothing.
	c := team(Member{Phone: alice})
	newcomer := "6287777777777@s.whatsapp.net"
	d := c.Decide(Incoming{ChatID: group, From: newcomer, Body: "@" + botNum + " run this"}, bot)
	if d.Allow || d.Reason != "team:not_approved" {
		t.Fatalf("newcomer: allow=%v reason=%q", d.Allow, d.Reason)
	}
	if c.Decide(Incoming{ChatID: group, From: aliceJ, Body: "@" + botNum + " hi"}, bot).Allow != true {
		t.Fatal("approved member should be allowed")
	}
}

func TestTeamWithEmptyRosterAllowsNobody(t *testing.T) {
	c := team()
	if c.Decide(Incoming{ChatID: group, From: aliceJ, Body: "@" + botNum + " hi"}, bot).Allow {
		t.Fatal("empty roster must allow nobody")
	}
}

func TestMentionIsStrippedFromThePrompt(t *testing.T) {
	c := team(Member{Phone: alice})
	d := c.Decide(Incoming{ChatID: group, From: aliceJ, Body: "@" + botNum + "   tolong cek @" + alice + " punya PR"}, bot)
	if !d.Allow {
		t.Fatalf("denied: %s", d.Reason)
	}
	if want := "tolong cek @" + alice + " punya PR"; d.Body != want {
		t.Fatalf("body = %q, want %q (bot mention removed, other mentions kept)", d.Body, want)
	}
}

func TestAuthorizeIgnoresTheMention(t *testing.T) {
	// Used when replaying the queue: the mention was already verified when the
	// message first arrived, membership must still be re-checked.
	c := team(Member{Phone: alice})
	if !c.Authorize(Incoming{ChatID: group, From: aliceJ, Body: "no mention here"}).Allow {
		t.Fatal("approved member should pass Authorize")
	}
	if c.Authorize(Incoming{ChatID: group, From: mallory}).Allow {
		t.Fatal("non-member must not pass Authorize")
	}
}

func TestNormalizeNumber(t *testing.T) {
	good := map[string]string{
		"6281234567890":                   "6281234567890",
		"+62 812-3456-7890":               "6281234567890",
		"6281234567890@s.whatsapp.net":    "6281234567890",
		"6281234567890:12@s.whatsapp.net": "6281234567890",
	}
	for in, want := range good {
		if got, err := NormalizeNumber(in); err != nil || got != want {
			t.Errorf("NormalizeNumber(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0812345678", "123", "abc", "62812abc4567", "6281234567890123456"} {
		if got, err := NormalizeNumber(bad); err == nil {
			t.Errorf("NormalizeNumber(%q) = %q, want an error", bad, got)
		}
	}
}

func TestNormalizeConfig(t *testing.T) {
	c := Config{Mode: ModeTeam, Team: Team{
		Group: "120363012345678901:5@g.us", Name: "  Dev\tteam \n",
		Members: []Member{
			{Phone: "+62 812-0000-0002", LID: "100000000000002@lid", Name: "Alice\nAdmin"},
			{Phone: "6281200000002", Name: "duplicate"},
		},
	}}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if c.Team.Group != group || c.Team.Name != "Dev team" {
		t.Errorf("group/name not canonical: %q %q", c.Team.Group, c.Team.Name)
	}
	if len(c.Team.Members) != 1 || c.Team.Members[0] != (Member{Phone: alice, LID: aliceL, Name: "Alice Admin"}) {
		t.Errorf("members = %+v", c.Team.Members)
	}

	for name, bad := range map[string]Config{
		"team without group":   {Mode: ModeTeam},
		"team with DM jid":     {Mode: ModeTeam, Team: Team{Group: ownerJ}},
		"personal no number":   {Mode: ModePersonal},
		"personal bad number":  {Mode: ModePersonal, Personal: Personal{Number: "0812"}},
		"unknown mode":         {Mode: "everyone"},
		"member without phone": {Mode: ModeTeam, Team: Team{Group: group, Members: []Member{{Name: "x"}}}},
	} {
		if err := bad.Normalize(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

func TestSwitchingModeKeepsTheOtherSection(t *testing.T) {
	c := Config{Mode: ModePersonal, Personal: Personal{Number: owner}, Team: Team{Group: group, Members: []Member{{Phone: alice}}}}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if c.Team.Group != group || len(c.Team.Members) != 1 {
		t.Fatalf("inactive team section was lost: %+v", c.Team)
	}
}

func TestLegacyFromEnv(t *testing.T) {
	c, ignored := Legacy([]string{ownerJ, alice + "@s.whatsapp.net", "garbage"})
	if c.Mode != ModePersonal || c.Personal.Number != owner {
		t.Fatalf("legacy config = %+v", c)
	}
	if len(ignored) != 2 {
		t.Errorf("personal mode has exactly one number; ignored = %v", ignored)
	}
}

func TestStorePersistsAndHotReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "access.json")
	legacy, _ := Legacy([]string{ownerJ})
	st := Open(path, legacy)

	if st.Source() != "env" || st.Get().Personal.Number != owner {
		t.Fatalf("before any file exists the env policy applies, got %q %+v", st.Source(), st.Get())
	}

	saved, err := st.Save(team(Member{Phone: alice, Name: "Alice"}))
	if err != nil {
		t.Fatal(err)
	}
	if saved.UpdatedAt == "" {
		t.Error("UpdatedAt not set")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("access file mode = %v, want 0600", fi.Mode().Perm())
	}
	if st.Source() != "file" || st.Get().Mode != ModeTeam {
		t.Fatalf("after Save: %q %+v", st.Source(), st.Get())
	}

	// A second Store on the same file (the bridge, next to the admin process)
	// sees the change without a restart.
	other := Open(path, legacy)
	if other.Get().Mode != ModeTeam {
		t.Fatalf("second store did not load the file: %+v", other.Get())
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := st.Save(personal()); err != nil {
		t.Fatal(err)
	}
	if other.Get().Mode != ModePersonal {
		t.Fatalf("second store did not pick up the edit: %+v", other.Get())
	}
}

func TestCorruptFileFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.json")
	legacy, _ := Legacy([]string{ownerJ})
	st := Open(path, legacy)
	if !st.Get().Decide(Incoming{ChatID: ownerJ, From: ownerJ, Body: "hi"}, bot).Allow {
		t.Fatal("env policy should apply first")
	}

	for name, content := range map[string]string{
		"not json":         "{ nope",
		"invalid contents": `{"mode":"team","team":{"group":"not-a-group"}}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
		if st.Get().Decide(Incoming{ChatID: ownerJ, From: ownerJ, Body: "hi"}, bot).Allow {
			t.Errorf("%s: a broken access file must deny everything, not fall back to the env policy", name)
		}
		if st.Err() == nil || !strings.Contains(st.Err().Error(), "access.json") {
			t.Errorf("%s: Err() should explain the problem, got %v", name, st.Err())
		}
	}
}

func TestRemovingTheFileReturnsToTheEnvPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.json")
	legacy, _ := Legacy([]string{ownerJ})
	st := Open(path, legacy)
	if _, err := st.Save(team()); err != nil {
		t.Fatal(err)
	}
	os.Remove(path)
	if st.Source() != "env" || st.Get().Mode != ModePersonal {
		t.Fatalf("after deleting the file: %q %+v", st.Source(), st.Get())
	}
}
