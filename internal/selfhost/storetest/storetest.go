package storetest

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"usesesame.app/backend/internal/authkit"
	"usesesame.app/backend/internal/selfhost"
)

const (
	testPassword      = "correct horse battery staple"
	otherTestPassword = "another long fictional passphrase"
)

type Config struct {
	Path     string
	AdminKey []byte
	Now      func() time.Time
	Flags    []selfhost.FlagDefinition
	ReadOnly bool
}

type Harness struct {
	Open                 func(t *testing.T, config Config) selfhost.Store
	TryOpen              func(config Config) (selfhost.Store, error)
	Raw                  func(t *testing.T, path string) *sql.DB
	InterruptedMigration func(t *testing.T, path string, key []byte)
}

type Clock struct {
	mu  sync.Mutex
	now time.Time
}

func NewClock() *Clock {
	return &Clock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Clock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

var testFlags = []selfhost.FlagDefinition{
	{Key: "sync", Description: "Sync", Default: false},
	{Key: "metrics", Description: "Metrics", Default: true},
}

type fixture struct {
	t       *testing.T
	h       Harness
	ctx     context.Context
	path    string
	key     []byte
	clock   *Clock
	store   selfhost.Store
	secrets map[string]string
}

func newFixture(t *testing.T, h Harness) *fixture {
	t.Helper()
	f := &fixture{
		t:       t,
		h:       h,
		ctx:     context.Background(),
		path:    filepath.Join(t.TempDir(), "sesame.db"),
		key:     bytes.Repeat([]byte{0x42}, 32),
		clock:   NewClock(),
		secrets: map[string]string{},
	}
	f.store = f.open()
	return f
}

func (f *fixture) config() Config {
	return Config{Path: f.path, AdminKey: f.key, Now: f.clock.Now, Flags: testFlags}
}

func (f *fixture) open() selfhost.Store {
	f.t.Helper()
	return f.h.Open(f.t, f.config())
}

func (f *fixture) reopen() {
	f.t.Helper()
	if err := f.store.Close(); err != nil {
		f.t.Fatal(err)
	}
	f.store = f.open()
}

func (f *fixture) raw() *sql.DB {
	f.t.Helper()
	return f.h.Raw(f.t, f.path)
}

func (f *fixture) step() {
	f.clock.Advance(31 * time.Second)
}

func (f *fixture) code(owner string) string {
	f.t.Helper()
	secret, ok := f.secrets[strings.ToLower(owner)]
	if !ok {
		f.t.Fatalf("no secret for %s", owner)
	}
	code, ok := authkit.TOTPCode(secret, f.clock.Now())
	if !ok {
		f.t.Fatal("cannot compute code")
	}
	return code
}

func (f *fixture) must(err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) expect(err, want error) {
	f.t.Helper()
	if !isError(err, want) {
		f.t.Fatalf("got error %v, want %v", err, want)
	}
}

func actorOf(login selfhost.OwnerLogin) selfhost.Actor {
	return selfhost.Actor{Kind: selfhost.ActorOwner, ID: login.Session.Owner.ID}
}

func (f *fixture) firstOwner(name string) selfhost.OwnerLogin {
	f.t.Helper()
	return f.firstOwnerChoosing(name, nil)
}

func (f *fixture) firstOwnerChoosing(name string, updateChecks *bool) selfhost.OwnerLogin {
	f.t.Helper()
	issue, err := f.store.StartFirstSetup(f.ctx)
	f.must(err)
	details, err := f.store.SetupDetails(f.ctx, issue.Token)
	f.must(err)
	f.secrets[strings.ToLower(name)] = details.TOTPSecret
	login, err := f.store.CompleteSetup(f.ctx, selfhost.CompleteSetupInput{Token: issue.Token, Name: name, Password: testPassword, Code: f.code(name), UpdateChecks: updateChecks})
	f.must(err)
	return login
}

func (f *fixture) invitedOwner(by selfhost.Actor, name string) selfhost.OwnerLogin {
	f.t.Helper()
	issue, err := f.store.InviteOwner(f.ctx, by, name)
	f.must(err)
	details, err := f.store.SetupDetails(f.ctx, issue.Token)
	f.must(err)
	f.secrets[strings.ToLower(name)] = details.TOTPSecret
	login, err := f.store.CompleteSetup(f.ctx, selfhost.CompleteSetupInput{Token: issue.Token, Password: otherTestPassword, Code: f.code(name)})
	f.must(err)
	return login
}

func (f *fixture) login(name, password string) (selfhost.OwnerLogin, error) {
	f.t.Helper()
	return f.store.Login(f.ctx, selfhost.LoginInput{Name: name, Password: password, Code: f.code(name)})
}

func (f *fixture) member(by selfhost.Actor, name string) selfhost.Member {
	f.t.Helper()
	member, err := f.store.CreateMember(f.ctx, by, name)
	f.must(err)
	return member
}

func (f *fixture) pair(by selfhost.Actor, holder selfhost.Holder, deviceName string) selfhost.IssuedDevice {
	f.t.Helper()
	issued, err := f.store.CreatePairing(f.ctx, by, selfhost.PairingInput{Holder: holder})
	f.must(err)
	device, err := f.store.RedeemPairing(f.ctx, selfhost.RedeemInput{Code: issued.Code, DeviceName: deviceName, Meta: selfhost.DeviceMeta{AppVersion: "0.3.1", Platform: "linux", ProtocolVersion: 1}})
	f.must(err)
	return device
}

func memberHolder(member selfhost.Member) selfhost.Holder {
	return selfhost.Holder{Kind: selfhost.HolderMember, ID: member.ID}
}

func ownerHolder(login selfhost.OwnerLogin) selfhost.Holder {
	return selfhost.Holder{Kind: selfhost.HolderOwner, ID: login.Session.Owner.ID}
}

func Run(t *testing.T, h Harness) {
	t.Helper()
	suites := []struct {
		name string
		run  func(*testing.T, Harness)
	}{
		{"setup", testSetup},
		{"login", testLogin},
		{"session", testSession},
		{"step up", testStepUp},
		{"owners", testOwners},
		{"members", testMembers},
		{"pairing", testPairing},
		{"devices", testDevices},
		{"audit", testAudit},
		{"flags and instance", testFlagsAndInstance},
		{"rate limits", testRateLimits},
		{"maintenance", testMaintenance},
		{"storage", testStorage},
		{"updates", testUpdates},
		{"schema", testSchema},
	}
	for _, suite := range suites {
		t.Run(suite.name, func(t *testing.T) {
			suite.run(t, h)
		})
	}
}

func each(t *testing.T, h Harness, name string, fn func(f *fixture)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Parallel()
		fn(newFixture(t, h))
	})
}
