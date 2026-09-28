package adminauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fast = 1000 // PBKDF2 iterations: cheap enough for tests

func newStore(t *testing.T) *Store {
	t.Helper()
	return Open(filepath.Join(t.TempDir(), "sub", "admin.json"), fast)
}

func TestSetVerifyAndNoPlaintextOnDisk(t *testing.T) {
	s := newStore(t)
	if exists, err := s.State(); exists || err != nil {
		t.Fatalf("fresh store: exists=%v err=%v", exists, err)
	}
	if s.Verify("admin", "whatever-long-enough") {
		t.Fatal("nothing is set yet, nobody may log in")
	}

	if err := s.Set("admin", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if !s.Verify("admin", "correct horse battery") {
		t.Fatal("right credentials rejected")
	}
	for name, c := range map[string][2]string{
		"wrong password":       {"admin", "correct horse batterY"},
		"wrong username":       {"root", "correct horse battery"},
		"different case user":  {"Admin", "correct horse battery"},
		"empty":                {"", ""},
		"password as username": {"correct horse battery", "admin"},
	} {
		if s.Verify(c[0], c[1]) {
			t.Errorf("%s was accepted", name)
		}
	}

	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "correct horse battery") {
		t.Fatal("the password is stored in the clear")
	}
	var c Credentials
	if err := json.Unmarshal(raw, &c); err != nil || c.Algo != "pbkdf2-sha256" || c.Iterations != fast || c.Salt == "" || c.Hash == "" {
		t.Fatalf("unexpected file contents: %s (%v)", raw, err)
	}
	if fi, _ := os.Stat(s.Path()); fi.Mode().Perm() != 0o600 {
		t.Errorf("credentials file mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestEverySetUsesAFreshSalt(t *testing.T) {
	s := newStore(t)
	_ = s.Set("admin", "correct horse battery")
	first := s.Fingerprint()
	_ = s.Set("admin", "correct horse battery")
	if s.Fingerprint() == first {
		t.Fatal("same password hashed twice must still get a new salt")
	}
}

func TestPasswordAndUsernameRules(t *testing.T) {
	for _, bad := range []string{"short", "admin1234", "aaaaaaaaaaaa", "abababababab"} {
		if err := ValidatePassword(bad, "admin"); err == nil {
			t.Errorf("password %q accepted", bad)
		}
	}
	if err := ValidatePassword("Administrator", "administrator"); err == nil {
		t.Error("a password equal to the username (any case) must be refused")
	}
	if err := ValidatePassword(strings.Repeat("a1b2c3", 40), "admin"); err == nil {
		t.Error("absurdly long passwords must be refused")
	}
	if err := ValidatePassword("correct horse battery", "admin"); err != nil {
		t.Errorf("good passphrase refused: %v", err)
	}

	for _, ok := range []string{"admin", "tarkiman", "a.b-c_9", "Bob"} {
		if err := ValidateUsername(ok); err != nil {
			t.Errorf("username %q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ab", strings.Repeat("x", 33), "with space", "semi;colon", "üser", "a/b"} {
		if err := ValidateUsername(bad); err == nil {
			t.Errorf("username %q accepted", bad)
		}
	}
	if err := newStore(t).Set("admin", "short"); err == nil {
		t.Error("Set must enforce the password policy")
	}
}

func TestChangesFromAnotherProcessAreSeen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.json")
	ui, cli := Open(path, fast), Open(path, fast) // the running admin and `bin/admin passwd`
	if err := cli.Set("admin", "first passphrase here"); err != nil {
		t.Fatal(err)
	}
	if !ui.Verify("admin", "first passphrase here") {
		t.Fatal("the running store did not see a file written by another process")
	}
	fp := ui.Fingerprint()
	time.Sleep(20 * time.Millisecond)
	if err := cli.Set("admin", "second passphrase here"); err != nil {
		t.Fatal(err)
	}
	if ui.Verify("admin", "first passphrase here") || !ui.Verify("admin", "second passphrase here") {
		t.Fatal("password change made elsewhere was not picked up")
	}
	if ui.Fingerprint() == fp {
		t.Fatal("fingerprint must change with the password so sessions are invalidated")
	}
}

func TestBrokenFileIsNotTreatedAsNoPasswordYet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.json")
	for name, content := range map[string]string{
		"not json":        "{ nope",
		"wrong algorithm": `{"username":"admin","algo":"md5","iterations":1,"salt":"YQ==","hash":"YQ=="}`,
		"missing fields":  `{"algo":"pbkdf2-sha256","iterations":1}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		s := Open(path, fast)
		if exists, err := s.State(); exists || err == nil {
			t.Errorf("%s: exists=%v err=%v — a broken file must be reported, not mistaken for 'not set up yet'", name, exists, err)
		}
		if s.Verify("admin", "anything at all") {
			t.Errorf("%s: login must fail", name)
		}
	}
	// ...and it can be repaired by setting a password again.
	s := Open(path, fast)
	if err := s.Set("admin", "repaired passphrase"); err != nil {
		t.Fatal(err)
	}
	if exists, err := s.State(); !exists || err != nil {
		t.Fatalf("after repair: exists=%v err=%v", exists, err)
	}
}

func TestBootstrapAdoptsTheOldEnvPasswordOnce(t *testing.T) {
	s := newStore(t)
	if adopted, err := Bootstrap(s, "", ""); adopted || err != nil {
		t.Fatalf("empty password must adopt nothing: %v %v", adopted, err)
	}
	adopted, err := Bootstrap(s, "", "s3cret") // shorter than the new policy on purpose
	if err != nil || !adopted {
		t.Fatalf("adopted=%v err=%v", adopted, err)
	}
	if !s.Verify("admin", "s3cret") {
		t.Fatal("the adopted password must keep working")
	}
	raw, _ := os.ReadFile(s.Path())
	if strings.Contains(string(raw), "s3cret") {
		t.Fatal("adopted password stored in the clear")
	}
	if err := s.Set("admin", "a much better passphrase"); err != nil {
		t.Fatal(err)
	}
	if adopted, _ := Bootstrap(s, "admin", "s3cret"); adopted {
		t.Fatal("bootstrap must never overwrite existing credentials")
	}
	if !s.Verify("admin", "a much better passphrase") || s.Verify("admin", "s3cret") {
		t.Fatal("existing credentials were changed by bootstrap")
	}
	if _, err := Bootstrap(newStore(t), "bad user!", "s3cret"); err == nil {
		t.Error("an invalid username must be refused")
	}
}

func TestSessionsExpireAndFollowThePassword(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ss := NewSessions(30*time.Minute, 12*time.Hour)
	ss.now = func() time.Time { return now }

	tok := ss.Create("fp1")
	if !ss.Valid(tok, "fp1") {
		t.Fatal("fresh session invalid")
	}
	if ss.Valid("", "fp1") || ss.Valid("not-a-token", "fp1") || ss.Valid(tok, "") {
		t.Fatal("bogus tokens / empty fingerprint accepted")
	}
	if ss.Valid(tok, "fp2") {
		t.Fatal("session survived a password change (fingerprint differs)")
	}

	tok = ss.Create("fp1")
	now = now.Add(20 * time.Minute)
	if !ss.Valid(tok, "fp1") { // activity extends the idle timer
		t.Fatal("session died early")
	}
	now = now.Add(20 * time.Minute)
	if !ss.Valid(tok, "fp1") {
		t.Fatal("activity should have reset the idle timer")
	}
	now = now.Add(31 * time.Minute)
	if ss.Valid(tok, "fp1") {
		t.Fatal("idle session should have expired")
	}

	tok = ss.Create("fp1")
	for i := 0; i < 26; i++ { // stay active, but hit the absolute limit
		now = now.Add(29 * time.Minute)
		if i < 24 && !ss.Valid(tok, "fp1") {
			t.Fatalf("session died at step %d, before the 12h limit", i)
		}
	}
	if ss.Valid(tok, "fp1") {
		t.Fatal("session outlived the absolute 12h limit")
	}

	a, b := ss.Create("fp"), ss.Create("fp")
	ss.Revoke(a)
	if ss.Valid(a, "fp") || !ss.Valid(b, "fp") {
		t.Fatal("Revoke must affect only that session")
	}
	ss.RevokeAll()
	if ss.Valid(b, "fp") {
		t.Fatal("RevokeAll left a session alive")
	}
}

func TestSessionTokensAreLongAndUnique(t *testing.T) {
	ss := NewSessions(time.Hour, time.Hour)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tok := ss.Create("fp")
		if len(tok) < 40 || seen[tok] {
			t.Fatalf("weak or repeated token %q", tok)
		}
		seen[tok] = true
	}
}

func TestLimiterLocksOutAndBacksOff(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(3, 10*time.Minute, 5*time.Minute)
	l.now = func() time.Time { return now }

	for i := 0; i < 2; i++ {
		l.Fail("1.2.3.4")
		if ok, _ := l.Check("1.2.3.4"); !ok {
			t.Fatalf("locked after only %d failures", i+1)
		}
	}
	l.Fail("1.2.3.4")
	ok, wait := l.Check("1.2.3.4")
	if ok || wait != 5*time.Minute {
		t.Fatalf("after 3 failures: ok=%v wait=%v, want locked for 5m", ok, wait)
	}
	if ok, _ := l.Check("5.6.7.8"); !ok {
		t.Fatal("another client must not be affected")
	}

	now = now.Add(5*time.Minute + time.Second)
	if ok, _ := l.Check("1.2.3.4"); !ok {
		t.Fatal("lock should have ended")
	}
	for i := 0; i < 3; i++ {
		l.Fail("1.2.3.4")
	}
	if _, wait := l.Check("1.2.3.4"); wait != 10*time.Minute {
		t.Fatalf("second lockout should double to 10m, got %v", wait)
	}

	l.Success("1.2.3.4")
	if ok, _ := l.Check("1.2.3.4"); !ok {
		t.Fatal("a successful login must clear the lock")
	}

	// Failures spread beyond the window don't add up.
	for i := 0; i < 2; i++ {
		l.Fail("9.9.9.9")
		now = now.Add(11 * time.Minute)
	}
	l.Fail("9.9.9.9")
	if ok, _ := l.Check("9.9.9.9"); !ok {
		t.Fatal("old failures outside the window must not count")
	}

	// The lock is capped at one hour.
	for round := 0; round < 8; round++ {
		now = now.Add(2 * time.Hour)
		for i := 0; i < 3; i++ {
			l.Fail("6.6.6.6")
		}
	}
	if _, wait := l.Check("6.6.6.6"); wait > time.Hour {
		t.Fatalf("lock exceeded one hour: %v", wait)
	}
}
