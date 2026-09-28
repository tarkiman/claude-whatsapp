package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tarkiman/claude-whatsapp/internal/access"
	"github.com/tarkiman/claude-whatsapp/internal/config"
	"github.com/tarkiman/claude-whatsapp/internal/gowa"
)

const (
	tGroup = "120363012345678901@g.us"
	tBot   = "6289500000001"
)

// accessRig is an admin server whose gowa is a stub with one device, a few
// groups and one group's member list — including a member gowa only knows by
// LID and one with a hostile display name.
type accessRig struct {
	t    *testing.T
	s    *Server
	file string
}

func newAccessRig(t *testing.T, legacySenders ...string) *accessRig {
	t.Helper()
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/devices":
			_, _ = w.Write([]byte(`{"code":"SUCCESS","results":[{"id":"dev1","display_name":"","jid":"` + tBot + `:37@s.whatsapp.net","state":"connected"}]}`))
		case "/user/my/groups":
			_, _ = w.Write([]byte(`{"code":"SUCCESS","results":{"data":[{"JID":"120363999999999999@g.us","Name":"Zeta"},{"JID":"` + tGroup + `","Name":"Alpha team"}]}}`))
		case "/group/participants":
			_, _ = w.Write([]byte(`{"code":"SUCCESS","results":{"group_id":"` + tGroup + `","name":"Alpha team","participants":[
				{"jid":"100000000000009@lid","phone_number":"` + tBot + `","lid":"100000000000009@lid","display_name":"bot"},
				{"jid":"100000000000002@lid","phone_number":"6281200000002","lid":"100000000000002@lid","display_name":"Alice","is_admin":true},
				{"jid":"111111111111111@lid","phone_number":"6281234567890","lid":"111111111111111@lid","display_name":"Bob\nthe \"builder\""},
				{"jid":"222222222222222@lid","phone_number":"","lid":"222222222222222@lid","display_name":"Ghost"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gw.Close)

	file := filepath.Join(t.TempDir(), "access.json")
	cfg := &config.Config{AccessFile: file, AllowedSenders: legacySenders}
	return &accessRig{t: t, s: New(cfg, gowa.New(gw.URL, "", ""), Options{}), file: file}
}

func (a *accessRig) do(method, path, body string) (int, []byte) {
	a.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5000"
	req.Host = "localhost:8098"
	if method == http.MethodPost {
		req.Header.Set(adminHeader, "1")
	}
	rec := httptest.NewRecorder()
	a.s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (a *accessRig) view() accessView {
	a.t.Helper()
	code, body := a.do(http.MethodGet, "/api/access", "")
	if code != 200 {
		a.t.Fatalf("GET /api/access: %d %s", code, body)
	}
	var v accessView
	if err := json.Unmarshal(body, &v); err != nil {
		a.t.Fatal(err)
	}
	return v
}

func (a *accessRig) members(group string) []memberView {
	a.t.Helper()
	code, body := a.do(http.MethodGet, "/api/access/members?group="+group, "")
	if code != 200 {
		a.t.Fatalf("GET members: %d %s", code, body)
	}
	var out struct {
		Members []memberView `json:"members"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		a.t.Fatal(err)
	}
	return out.Members
}

func TestAccessStartsFromTheEnvironmentNumber(t *testing.T) {
	a := newAccessRig(t, "6281234567890@s.whatsapp.net")
	v := a.view()
	if v.Source != "env" || v.Config.Mode != access.ModePersonal || v.Config.Personal.Number != "6281234567890" {
		t.Fatalf("view = %+v", v)
	}
	if v.BotPhone != tBot {
		t.Errorf("botPhone = %q, want %q", v.BotPhone, tBot)
	}
	if _, err := os.Stat(a.file); err == nil {
		t.Error("merely viewing must not write the access file")
	}
}

func TestSavePersonalNumberIsNormalisedAndPersisted(t *testing.T) {
	a := newAccessRig(t)
	code, body := a.do(http.MethodPost, "/api/access", `{"mode":"personal","personalNumber":"+62 812-3456-7890"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	v := a.view()
	if v.Source != "file" || v.Config.Personal.Number != "6281234567890" {
		t.Fatalf("after save: %+v", v)
	}
	if fi, err := os.Stat(a.file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("access file: %v, %v", fi, err)
	}
}

func TestSaveRejectsInvalidInputAndKeepsThePreviousPolicy(t *testing.T) {
	a := newAccessRig(t)
	if code, _ := a.do(http.MethodPost, "/api/access", `{"mode":"personal","personalNumber":"6281234567890"}`); code != 200 {
		t.Fatal("setup failed")
	}
	bad := map[string]string{
		"personal, number starts with 0": `{"mode":"personal","personalNumber":"0812345678"}`,
		"personal, letters":              `{"mode":"personal","personalNumber":"abc"}`,
		"team without a group":           `{"mode":"team"}`,
		"team with a DM as the group":    `{"mode":"team","team":{"group":"6281234567890@s.whatsapp.net"}}`,
		"unknown mode":                   `{"mode":"everyone"}`,
		"member without a phone":         `{"mode":"team","team":{"group":"` + tGroup + `","members":[{"name":"x"}]}}`,
		"not json":                       `{`,
	}
	for name, body := range bad {
		if code, _ := a.do(http.MethodPost, "/api/access", body); code != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d, want 400", name, code)
		}
	}
	if v := a.view(); v.Config.Mode != access.ModePersonal || v.Config.Personal.Number != "6281234567890" {
		t.Fatalf("a rejected save changed the policy: %+v", v.Config)
	}
}

func TestSavePostNeedsTheAdminHeader(t *testing.T) {
	a := newAccessRig(t)
	req := httptest.NewRequest(http.MethodPost, "/api/access", strings.NewReader(`{"mode":"personal","personalNumber":"6281234567890"}`))
	req.RemoteAddr, req.Host = "127.0.0.1:5000", "localhost:8098"
	rec := httptest.NewRecorder()
	a.s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST without %s: HTTP %d, want 403", adminHeader, rec.Code)
	}
}

func TestGroupsAreListedByName(t *testing.T) {
	a := newAccessRig(t)
	code, body := a.do(http.MethodGet, "/api/access/groups", "")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var groups []gowa.Group
	_ = json.Unmarshal(body, &groups)
	if len(groups) != 2 || groups[0].Name != "Alpha team" || groups[0].JID != tGroup {
		t.Fatalf("groups = %+v", groups)
	}
}

func TestMemberListShowsApprovalAndFlagsTheBotAndPhonelessMembers(t *testing.T) {
	a := newAccessRig(t)
	if code, body := a.do(http.MethodPost, "/api/access", `{"mode":"team","team":{"group":"`+tGroup+`","name":"Alpha team","members":[{"phone":"6281200000002","lid":"100000000000002","name":"Alice"}]}}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	members := a.members(tGroup)
	byName := map[string]memberView{}
	for _, m := range members {
		byName[m.Name] = m
	}
	if len(members) != 4 || !members[len(members)-1].IsBot {
		t.Fatalf("the bot should be listed last and flagged: %+v", members)
	}
	if m := byName["Alice"]; !m.Allowed || !m.IsAdmin || !m.Approvable {
		t.Errorf("Alice (approved) = %+v", m)
	}
	if m := byName["Bob the \"builder\""]; m.Allowed || !m.Approvable {
		t.Errorf("Bob joined but was never approved, so he must be listed as not allowed: %+v", m)
	}
	if m := byName["Ghost"]; m.Approvable || m.Allowed {
		t.Errorf("a member gowa knows only by LID cannot be approved: %+v", m)
	}
	if m := byName["bot"]; !m.IsBot || m.Allowed {
		t.Errorf("bot row = %+v", m)
	}
	for _, m := range members {
		if strings.ContainsAny(m.Name, "\n\r") {
			t.Errorf("display names must be cleaned, got %q", m.Name)
		}
	}
}

func TestApprovalIsPerGroup(t *testing.T) {
	// A roster approved for one group must not light up ticks for another.
	a := newAccessRig(t)
	if code, _ := a.do(http.MethodPost, "/api/access", `{"mode":"team","team":{"group":"120363999999999999@g.us","members":[{"phone":"6281200000002"}]}}`); code != 200 {
		t.Fatal("setup failed")
	}
	for _, m := range a.members(tGroup) {
		if m.Allowed {
			t.Fatalf("%s shows as allowed in a group the roster was not saved for", m.Name)
		}
	}
}

func TestSwitchingModeKeepsTheOtherSectionAndTakesEffectInThePolicy(t *testing.T) {
	a := newAccessRig(t)
	a.do(http.MethodPost, "/api/access", `{"mode":"team","team":{"group":"`+tGroup+`","members":[{"phone":"6281200000002"}]}}`)
	a.do(http.MethodPost, "/api/access", `{"mode":"personal","personalNumber":"6281234567890"}`)

	v := a.view()
	if v.Config.Mode != access.ModePersonal || len(v.Config.Team.Members) != 1 || v.Config.Team.Group != tGroup {
		t.Fatalf("switching to personal lost the team roster: %+v", v.Config)
	}
	// And the policy the bridge would read now really is personal-only.
	bridgeView := access.Open(a.file, access.Config{})
	d := bridgeView.Get().Decide(access.Incoming{ChatID: tGroup, From: "6281200000002@s.whatsapp.net", Body: "@" + tBot + " hi"}, access.Bot{Phone: tBot})
	if d.Allow {
		t.Fatal("personal mode must ignore groups even though a team roster is stored")
	}
}

func TestSavingOverABrokenFileStartsClean(t *testing.T) {
	a := newAccessRig(t, "6281234567890")
	if err := os.WriteFile(a.file, []byte("{ nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v := a.view(); v.Error == "" {
		t.Fatal("a broken file should be reported to the UI")
	}
	if code, body := a.do(http.MethodPost, "/api/access", `{"mode":"personal","personalNumber":"6281234567890"}`); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if v := a.view(); v.Error != "" || v.Config.Personal.Number != "6281234567890" {
		t.Fatalf("after saving: %+v", v)
	}
}

func TestMembersRejectsANonGroupJID(t *testing.T) {
	a := newAccessRig(t)
	if code, _ := a.do(http.MethodGet, "/api/access/members?group=6281234567890@s.whatsapp.net", ""); code != http.StatusBadRequest {
		t.Fatalf("HTTP %d, want 400", code)
	}
}
