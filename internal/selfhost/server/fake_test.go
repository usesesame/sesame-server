package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"usesesame.app/backend/internal/authkit"
	"usesesame.app/backend/internal/selfhost"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock {
	return &clock{now: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fakeOwner struct {
	owner       selfhost.Owner
	password    string
	secret      string
	lastCounter int64
	removed     bool
}

type fakeSetup struct {
	ownerID   string
	first     bool
	expiresAt time.Time
	used      bool
}

type fakeSession struct {
	session selfhost.OwnerSession
	ownerID string
}

type fakePairing struct {
	pairing   selfhost.Pairing
	used      bool
	cancelled bool
}

type fakeStore struct {
	mu                                sync.Mutex
	fullVerifies, incrementalVerifies int
	clock                             *clock
	instance                          selfhost.Instance
	owners                            []*fakeOwner
	setups                            map[string]*fakeSetup
	sessions                          map[string]*fakeSession
	members                           []selfhost.Member
	pairings                          map[string]*fakePairing
	devices                           []*selfhost.Device
	tokens                            map[string]string
	flags                             map[string]bool
	audit                             []selfhost.AuditEntry
	limits                            map[string][]time.Time
	counter                           int
	pingErr                           error
	panicPing                         bool
	backupAt                          *time.Time
	updates                           selfhost.UpdateSettings
}

func newFakeStore(c *clock) *fakeStore {
	return &fakeStore{
		clock:    c,
		instance: selfhost.Instance{ID: "inst0001", Name: "Home server", CreatedAt: c.Now(), UpdatedAt: c.Now()},
		setups:   map[string]*fakeSetup{},
		sessions: map[string]*fakeSession{},
		pairings: map[string]*fakePairing{},
		tokens:   map[string]string{},
		flags:    map[string]bool{"desktop_linking_enabled": true},
		limits:   map[string][]time.Time{},
		updates:  selfhost.UpdateSettings{Choice: selfhost.UpdatesUnset, Channel: selfhost.DefaultUpdateChannel},
	}
}

func (f *fakeStore) nextID(prefix string) string {
	f.counter++
	return fmt.Sprintf("%s%04d", prefix, f.counter)
}

func (f *fakeStore) newToken() string {
	token, _, err := authkit.NewToken()
	if err != nil {
		panic(err)
	}
	return token
}

func (f *fakeStore) record(by selfhost.Actor, action, target string) {
	var previous string
	if len(f.audit) > 0 {
		previous = f.audit[len(f.audit)-1].Hash
	}
	sum := sha256.Sum256([]byte(previous + by.String() + action + target + fmt.Sprint(len(f.audit))))
	f.audit = append(f.audit, selfhost.AuditEntry{
		Seq: int64(len(f.audit) + 1), Actor: by.String(), Action: action, Target: target, Detail: map[string]string{},
		At: f.clock.Now(), PrevHash: previous, Hash: hex.EncodeToString(sum[:]),
	})
}

func (f *fakeStore) activeOwners() int {
	count := 0
	for _, owner := range f.owners {
		if !owner.removed && !owner.owner.SetupPending {
			count++
		}
	}
	return count
}

func (f *fakeStore) Instance(context.Context) (selfhost.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.instance, nil
}

func (f *fakeStore) UpdateInstance(_ context.Context, by selfhost.Actor, update selfhost.InstanceUpdate) (selfhost.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if update.Name != nil {
		f.instance.Name = *update.Name
	}
	if update.PublicURL != nil {
		f.instance.PublicURL = *update.PublicURL
	}
	f.record(by, "settings.updated", "instance")
	return f.instance, nil
}

func (f *fakeStore) SetupRequired(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activeOwners() == 0, nil
}

func (f *fakeStore) issueSetup(name string, first bool) (selfhost.SetupIssue, error) {
	secret, err := authkit.NewTOTPSecret()
	if err != nil {
		return selfhost.SetupIssue{}, err
	}
	owner := &fakeOwner{owner: selfhost.Owner{ID: f.nextID("own"), Name: name, CreatedAt: f.clock.Now(), SetupPending: true}, secret: secret}
	f.owners = append(f.owners, owner)
	token := f.newToken()
	expires := f.clock.Now().Add(selfhost.SetupTokenTTL)
	f.setups[token] = &fakeSetup{ownerID: owner.owner.ID, first: first, expiresAt: expires}
	return selfhost.SetupIssue{Owner: owner.owner, Token: token, ExpiresAt: expires}, nil
}

func (f *fakeStore) StartFirstSetup(context.Context) (selfhost.SetupIssue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.issueSetup("", true)
}

func (f *fakeStore) InviteOwner(_ context.Context, by selfhost.Actor, name string) (selfhost.SetupIssue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, owner := range f.owners {
		if !owner.removed && selfhost.NameKey(owner.owner.Name) == selfhost.NameKey(name) {
			return selfhost.SetupIssue{}, selfhost.ErrConflict
		}
	}
	issue, err := f.issueSetup(name, false)
	f.record(by, "owner.invited", name)
	return issue, err
}

func (f *fakeStore) ResetOwner(context.Context, selfhost.Actor, string) (selfhost.SetupIssue, error) {
	return selfhost.SetupIssue{}, selfhost.ErrNotFound
}

func (f *fakeStore) findSetup(token string) (*fakeSetup, *fakeOwner, error) {
	setup, ok := f.setups[token]
	if !ok || setup.used || !f.clock.Now().Before(setup.expiresAt) {
		return nil, nil, selfhost.ErrSetupTokenInvalid
	}
	for _, owner := range f.owners {
		if owner.owner.ID == setup.ownerID {
			return setup, owner, nil
		}
	}
	return nil, nil, selfhost.ErrSetupTokenInvalid
}

func (f *fakeStore) SetupDetails(_ context.Context, token string) (selfhost.SetupDetails, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	setup, owner, err := f.findSetup(token)
	if err != nil {
		return selfhost.SetupDetails{}, err
	}
	return selfhost.SetupDetails{TOTPSecret: owner.secret, OwnerName: owner.owner.Name, FirstOwner: setup.first, ExpiresAt: setup.expiresAt}, nil
}

func (f *fakeStore) newSession(owner *fakeOwner) selfhost.OwnerLogin {
	token := f.newToken()
	now := f.clock.Now()
	session := selfhost.OwnerSession{
		ID: f.nextID("ses"), Owner: owner.owner, CreatedAt: now, ExpiresAt: now.Add(selfhost.DefaultSessionTTL),
		RecentAuthAt: now, CSRFToken: f.newToken(),
	}
	f.sessions[token] = &fakeSession{session: session, ownerID: owner.owner.ID}
	return selfhost.OwnerLogin{Token: token, Session: session}
}

func (f *fakeStore) CompleteSetup(_ context.Context, input selfhost.CompleteSetupInput) (selfhost.OwnerLogin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	setup, owner, err := f.findSetup(input.Token)
	if err != nil {
		return selfhost.OwnerLogin{}, err
	}
	counter, ok := authkit.VerifyTOTP(owner.secret, input.Code, f.clock.Now())
	if !ok {
		return selfhost.OwnerLogin{}, selfhost.ErrInvalidCredentials
	}
	if owner.owner.Name == "" || setup.first {
		for _, other := range f.owners {
			if other != owner && !other.removed && selfhost.NameKey(other.owner.Name) == selfhost.NameKey(input.Name) {
				return selfhost.OwnerLogin{}, selfhost.ErrConflict
			}
		}
		owner.owner.Name = input.Name
	}
	owner.password = input.Password
	owner.lastCounter = counter
	owner.owner.SetupPending = false
	setup.used = true
	login := f.newSession(owner)
	f.record(selfhost.Actor{Kind: selfhost.ActorOwner, ID: owner.owner.ID}, "owner.setup_completed", owner.owner.ID)
	if setup.first && input.UpdateChecks != nil {
		f.updates.Choice = selfhost.UpdatesOff
		if *input.UpdateChecks {
			f.updates.Choice = selfhost.UpdatesOn
		}
		f.record(selfhost.Actor{Kind: selfhost.ActorOwner, ID: owner.owner.ID}, "updates.updated", "instance")
	}
	return login, nil
}

func (f *fakeStore) Login(_ context.Context, input selfhost.LoginInput) (selfhost.OwnerLogin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var found *fakeOwner
	for _, owner := range f.owners {
		if !owner.removed && !owner.owner.SetupPending && selfhost.NameKey(owner.owner.Name) == selfhost.NameKey(input.Name) {
			found = owner
		}
	}
	if found == nil || found.password != input.Password {
		return selfhost.OwnerLogin{}, selfhost.ErrInvalidCredentials
	}
	counter, ok := authkit.VerifyTOTP(found.secret, input.Code, f.clock.Now())
	if !ok {
		return selfhost.OwnerLogin{}, selfhost.ErrInvalidCredentials
	}
	if counter <= found.lastCounter {
		return selfhost.OwnerLogin{}, selfhost.ErrReplayedCode
	}
	found.lastCounter = counter
	now := f.clock.Now()
	found.owner.LastLoginAt = &now
	return f.newSession(found), nil
}

func (f *fakeStore) liveSession(token string) (*fakeSession, error) {
	entry, ok := f.sessions[token]
	if !ok || !f.clock.Now().Before(entry.session.ExpiresAt) {
		return nil, selfhost.ErrSessionInvalid
	}
	for _, owner := range f.owners {
		if owner.owner.ID == entry.ownerID && !owner.removed {
			entry.session.Owner = owner.owner
			return entry, nil
		}
	}
	return nil, selfhost.ErrSessionInvalid
}

func (f *fakeStore) Session(_ context.Context, token string) (selfhost.OwnerSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry, err := f.liveSession(token)
	if err != nil {
		return selfhost.OwnerSession{}, err
	}
	return entry.session, nil
}

func (f *fakeStore) StepUp(_ context.Context, input selfhost.StepUpInput) (selfhost.OwnerSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry, err := f.liveSession(input.SessionToken)
	if err != nil {
		return selfhost.OwnerSession{}, err
	}
	var owner *fakeOwner
	for _, candidate := range f.owners {
		if candidate.owner.ID == entry.ownerID {
			owner = candidate
		}
	}
	if owner.password != input.Password {
		return selfhost.OwnerSession{}, selfhost.ErrInvalidCredentials
	}
	counter, ok := authkit.VerifyTOTP(owner.secret, input.Code, f.clock.Now())
	if !ok {
		return selfhost.OwnerSession{}, selfhost.ErrInvalidCredentials
	}
	if counter <= owner.lastCounter {
		return selfhost.OwnerSession{}, selfhost.ErrReplayedCode
	}
	owner.lastCounter = counter
	entry.session.RecentAuthAt = f.clock.Now()
	return entry.session, nil
}

func (f *fakeStore) Logout(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions[token]; !ok {
		return selfhost.ErrSessionInvalid
	}
	delete(f.sessions, token)
	return nil
}

func (f *fakeStore) ListOwners(context.Context) ([]selfhost.Owner, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owners := []selfhost.Owner{}
	for _, owner := range f.owners {
		if !owner.removed {
			owners = append(owners, owner.owner)
		}
	}
	return owners, nil
}

func (f *fakeStore) RemoveOwner(_ context.Context, by selfhost.Actor, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, owner := range f.owners {
		if owner.owner.ID != id || owner.removed {
			continue
		}
		if !owner.owner.SetupPending && f.activeOwners() <= 1 {
			return selfhost.ErrLastOwner
		}
		owner.removed = true
		for token, entry := range f.sessions {
			if entry.ownerID == id {
				delete(f.sessions, token)
			}
		}
		f.record(by, "owner.removed", id)
		return nil
	}
	return selfhost.ErrNotFound
}

func (f *fakeStore) CreateMember(_ context.Context, by selfhost.Actor, name string) (selfhost.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, member := range f.members {
		if selfhost.NameKey(member.Name) == selfhost.NameKey(name) {
			return selfhost.Member{}, selfhost.ErrConflict
		}
	}
	member := selfhost.Member{ID: f.nextID("mem"), Name: name, CreatedAt: f.clock.Now()}
	f.members = append(f.members, member)
	f.record(by, "member.created", member.ID)
	return member, nil
}

func (f *fakeStore) ListMembers(context.Context) ([]selfhost.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	members := append([]selfhost.Member{}, f.members...)
	for index := range members {
		for _, device := range f.devices {
			if device.Holder.ID == members[index].ID && device.RevokedAt == nil {
				members[index].DeviceCount++
			}
		}
	}
	return members, nil
}

func (f *fakeStore) RenameMember(_ context.Context, by selfhost.Actor, id, name string) (selfhost.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index := range f.members {
		if f.members[index].ID == id {
			f.members[index].Name = name
			f.record(by, "member.renamed", id)
			return f.members[index], nil
		}
	}
	return selfhost.Member{}, selfhost.ErrNotFound
}

func (f *fakeStore) DeleteMember(_ context.Context, by selfhost.Actor, id string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index := range f.members {
		if f.members[index].ID != id {
			continue
		}
		f.members = append(f.members[:index], f.members[index+1:]...)
		revoked := 0
		now := f.clock.Now()
		for _, device := range f.devices {
			if device.Holder.ID == id && device.RevokedAt == nil {
				device.RevokedAt = &now
				revoked++
			}
		}
		f.record(by, "member.deleted", id)
		return revoked, nil
	}
	return 0, selfhost.ErrNotFound
}

func (f *fakeStore) CreatePairing(_ context.Context, by selfhost.Actor, input selfhost.PairingInput) (selfhost.IssuedPairing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	code := f.newToken()
	now := f.clock.Now()
	pairing := selfhost.Pairing{ID: f.nextID("pai"), Holder: input.Holder, DeviceNameHint: input.DeviceNameHint, CreatedBy: by.ID, CreatedAt: now, ExpiresAt: now.Add(selfhost.PairingTTL)}
	f.pairings[code] = &fakePairing{pairing: pairing}
	f.record(by, "pairing.created", pairing.ID)
	return selfhost.IssuedPairing{Pairing: pairing, Code: code}, nil
}

func (f *fakeStore) ListPairings(context.Context) ([]selfhost.Pairing, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pairings := []selfhost.Pairing{}
	for _, entry := range f.pairings {
		if !entry.used && !entry.cancelled && f.clock.Now().Before(entry.pairing.ExpiresAt) {
			pairings = append(pairings, entry.pairing)
		}
	}
	return pairings, nil
}

func (f *fakeStore) CancelPairing(_ context.Context, by selfhost.Actor, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, entry := range f.pairings {
		if entry.pairing.ID == id && !entry.used && !entry.cancelled {
			entry.cancelled = true
			f.record(by, "pairing.cancelled", id)
			return nil
		}
	}
	return selfhost.ErrNotFound
}

func (f *fakeStore) RedeemPairing(_ context.Context, input selfhost.RedeemInput) (selfhost.IssuedDevice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry, ok := f.pairings[input.Code]
	if !ok || entry.used || entry.cancelled || !f.clock.Now().Before(entry.pairing.ExpiresAt) {
		return selfhost.IssuedDevice{}, selfhost.ErrPairingInvalid
	}
	entry.used = true
	now := f.clock.Now()
	device := &selfhost.Device{
		ID: f.nextID("dev"), Name: input.DeviceName, Holder: entry.pairing.Holder, Meta: input.Meta,
		CreatedAt: now, ExpiresAt: now.Add(selfhost.DeviceTokenTTL), LastSeenAt: now,
	}
	f.devices = append(f.devices, device)
	token := f.newToken()
	f.tokens[token] = device.ID
	f.record(selfhost.Actor{Kind: selfhost.ActorDevice, ID: device.ID}, "device.paired", device.ID)
	return selfhost.IssuedDevice{Device: *device, Token: token}, nil
}

func (f *fakeStore) deviceForToken(token string) (*selfhost.Device, error) {
	id, ok := f.tokens[token]
	if !ok {
		return nil, selfhost.ErrDeviceInvalid
	}
	for _, device := range f.devices {
		if device.ID == id {
			if device.RevokedAt != nil || !f.clock.Now().Before(device.ExpiresAt) {
				return nil, selfhost.ErrDeviceInvalid
			}
			return device, nil
		}
	}
	return nil, selfhost.ErrDeviceInvalid
}

func (f *fakeStore) AuthenticateDevice(_ context.Context, token string) (selfhost.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	device, err := f.deviceForToken(token)
	if err != nil {
		return selfhost.Device{}, err
	}
	return *device, nil
}

func (f *fakeStore) Heartbeat(_ context.Context, token string, meta selfhost.DeviceMeta) (selfhost.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	device, err := f.deviceForToken(token)
	if err != nil {
		return selfhost.Device{}, err
	}
	device.Meta = meta
	device.LastSeenAt = f.clock.Now()
	return *device, nil
}

func (f *fakeStore) Disconnect(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	device, err := f.deviceForToken(token)
	if err != nil {
		return err
	}
	now := f.clock.Now()
	device.RevokedAt = &now
	return nil
}

func (f *fakeStore) ListDevices(_ context.Context, filter selfhost.DeviceFilter) ([]selfhost.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	devices := []selfhost.Device{}
	for _, device := range f.devices {
		if filter.IncludeInactive || (device.RevokedAt == nil && f.clock.Now().Before(device.ExpiresAt)) {
			devices = append(devices, *device)
		}
	}
	return devices, nil
}

func (f *fakeStore) RevokeDevice(_ context.Context, by selfhost.Actor, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, device := range f.devices {
		if device.ID == id && device.RevokedAt == nil {
			now := f.clock.Now()
			device.RevokedAt = &now
			f.record(by, "device.revoked", id)
			return nil
		}
	}
	return selfhost.ErrNotFound
}

func (f *fakeStore) RevokeMemberDevices(context.Context, selfhost.Actor, string) (int, error) {
	return 0, nil
}

func (f *fakeStore) Flags(context.Context) ([]selfhost.Flag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	flags := []selfhost.Flag{}
	for key, enabled := range f.flags {
		flags = append(flags, selfhost.Flag{Key: key, Description: "fake", Enabled: enabled, UpdatedAt: f.clock.Now()})
	}
	return flags, nil
}

func (f *fakeStore) SetFlag(_ context.Context, by selfhost.Actor, key string, enabled bool) (selfhost.Flag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.flags[key]; !ok {
		return selfhost.Flag{}, selfhost.ErrNotFound
	}
	f.flags[key] = enabled
	f.record(by, "flag.updated", key)
	return selfhost.Flag{Key: key, Description: "fake", Enabled: enabled, UpdatedAt: f.clock.Now()}, nil
}

func (f *fakeStore) AppendAudit(_ context.Context, input selfhost.AuditInput) (selfhost.AuditEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(input.Actor, input.Action, input.Target)
	return f.audit[len(f.audit)-1], nil
}

func (f *fakeStore) ListAudit(_ context.Context, cursor int64, limit int) (selfhost.AuditPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	page := selfhost.AuditPage{Entries: []selfhost.AuditEntry{}}
	for index := len(f.audit) - 1; index >= 0; index-- {
		entry := f.audit[index]
		if cursor > 0 && entry.Seq >= cursor {
			continue
		}
		if len(page.Entries) == limit {
			page.NextCursor = page.Entries[len(page.Entries)-1].Seq
			break
		}
		page.Entries = append(page.Entries, entry)
	}
	return page, nil
}

func (f *fakeStore) VerifyAudit(context.Context) (selfhost.AuditReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fullVerifies++
	return f.auditReport(), nil
}

func (f *fakeStore) VerifyAuditIncremental(context.Context) (selfhost.AuditReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.incrementalVerifies++
	return f.auditReport(), nil
}

func (f *fakeStore) auditReport() selfhost.AuditReport {
	report := selfhost.AuditReport{OK: true, Rows: int64(len(f.audit))}
	if len(f.audit) > 0 {
		report.HeadSeq = f.audit[len(f.audit)-1].Seq
		report.HeadHash = f.audit[len(f.audit)-1].Hash
	}
	return report
}

func (f *fakeStore) RateLimit(_ context.Context, key string, limit int, window time.Duration) (selfhost.RateDecision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.clock.Now()
	kept := []time.Time{}
	for _, at := range f.limits[key] {
		if now.Sub(at) < window {
			kept = append(kept, at)
		}
	}
	if len(kept) >= limit {
		f.limits[key] = kept
		return selfhost.RateDecision{Allowed: false, RetryAfter: window - now.Sub(kept[0])}, nil
	}
	f.limits[key] = append(kept, now)
	return selfhost.RateDecision{Allowed: true, Remaining: limit - len(kept) - 1}, nil
}

func (f *fakeStore) ResetRateLimit(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.limits, key)
	return nil
}

func (f *fakeStore) Maintain(context.Context) (selfhost.MaintenanceReport, error) {
	return selfhost.MaintenanceReport{}, nil
}

func (f *fakeStore) System(context.Context) (selfhost.SystemInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	active := 0
	for _, device := range f.devices {
		if device.RevokedAt == nil {
			active++
		}
	}
	return selfhost.SystemInfo{SchemaVersion: 1, DatabaseBytes: 4096, LastBackupAt: f.backupAt, ActiveOwners: f.activeOwners(), Members: len(f.members), ActiveDevices: active}, nil
}

func (f *fakeStore) Check(context.Context) (selfhost.CheckReport, error) {
	return selfhost.CheckReport{}, nil
}

func (f *fakeStore) Backup(context.Context, string) error { return nil }

func (f *fakeStore) RecordBackup(context.Context, time.Time) error { return nil }

func (f *fakeStore) UpdateSettings(context.Context) (selfhost.UpdateSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.updates, nil
}

func (f *fakeStore) ConfigureUpdates(_ context.Context, by selfhost.Actor, change selfhost.UpdateChange) (selfhost.UpdateSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	next := f.updates
	if change.Enabled != nil {
		next.Choice = selfhost.UpdatesOff
		if *change.Enabled {
			next.Choice = selfhost.UpdatesOn
		}
	}
	if change.Channel != nil {
		if !selfhost.ValidUpdateChannel(*change.Channel) {
			return selfhost.UpdateSettings{}, selfhost.ErrInvalidInput
		}
		next.Channel = *change.Channel
	}
	if next.Choice != f.updates.Choice || next.Channel != f.updates.Channel {
		f.record(by, "updates.updated", "instance")
	}
	f.updates = next
	return next, nil
}

func (f *fakeStore) RecordUpdateFeed(_ context.Context, feed selfhost.UpdateFeed) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if feed.Sequence < f.updates.Sequences[feed.KeyID] {
		return selfhost.ErrConflict
	}
	checked := feed.CheckedAt
	next := map[string]int64{}
	for key, sequence := range f.updates.Sequences {
		next[key] = sequence
	}
	next[feed.KeyID] = feed.Sequence
	f.updates.Sequences = next
	f.updates.Feed = append([]byte(nil), feed.Raw...)
	f.updates.CheckedAt = &checked
	f.updates.LastError = ""
	return nil
}

func (f *fakeStore) RecordUpdateFailure(_ context.Context, code string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates.LastError = code
	return nil
}

func (f *fakeStore) ResetUpdateSequences(_ context.Context, by selfhost.Actor) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cleared := len(f.updates.Sequences)
	f.updates.Sequences = nil
	f.record(by, "updates.sequences_reset", "instance")
	return cleared, nil
}

func (f *fakeStore) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.panicPing {
		panic("simulated store failure")
	}
	return f.pingErr
}

func (f *fakeStore) Close() error { return nil }

var _ selfhost.Store = (*fakeStore)(nil)

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}
