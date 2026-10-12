package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"usesesame.app/backend/internal/selfhost"
)

const (
	defaultAuditLimit = 50
	maxAuditLimit     = 200
	maxIDLength       = 64
)

type nameRequest struct {
	Name string `json:"name"`
}

type pairingRequest struct {
	MemberID   string `json:"memberId"`
	Self       bool   `json:"self"`
	DeviceName string `json:"deviceName"`
}

type settingsRequest struct {
	Name      *string `json:"name"`
	PublicURL *string `json:"publicUrl"`
}

type flagRequest struct {
	Enabled *bool `json:"enabled"`
}

type holderView struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ownerRecord struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastLoginAt  *time.Time `json:"lastLoginAt"`
	SetupPending bool       `json:"setupPending"`
	Current      bool       `json:"current"`
}

type memberRecord struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"createdAt"`
	DeviceCount int       `json:"deviceCount"`
}

type pairingRecord struct {
	ID         string     `json:"id"`
	Holder     holderView `json:"holder"`
	DeviceName string     `json:"deviceName"`
	CreatedBy  string     `json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
}

type deviceRecord struct {
	ID                          string     `json:"id"`
	Name                        string     `json:"name"`
	Holder                      holderView `json:"holder"`
	AppVersion                  string     `json:"appVersion"`
	Platform                    string     `json:"platform"`
	Architecture                string     `json:"architecture"`
	UpdateChannel               string     `json:"updateChannel"`
	ProtocolVersion             int        `json:"protocolVersion"`
	BrowserHelperCapable        bool       `json:"browserHelperCapable"`
	BrowserHelperLastObservedAt *time.Time `json:"browserHelperLastObservedAt"`
	CreatedAt                   time.Time  `json:"createdAt"`
	ExpiresAt                   time.Time  `json:"expiresAt"`
	LastSeenAt                  time.Time  `json:"lastSeenAt"`
	RevokedAt                   *time.Time `json:"revokedAt"`
}

type auditRecord struct {
	Seq    int64             `json:"seq"`
	Actor  string            `json:"actor"`
	Action string            `json:"action"`
	Target string            `json:"target"`
	Detail map[string]string `json:"detail"`
	At     time.Time         `json:"at"`
	Hash   string            `json:"hash"`
}

type chainRecord struct {
	OK         bool        `json:"ok"`
	Rows       int64       `json:"rows"`
	HeadSeq    int64       `json:"headSeq"`
	HeadHash   string      `json:"headHash"`
	FirstBreak *breakEntry `json:"firstBreak"`
}

type breakEntry struct {
	Seq    int64  `json:"seq"`
	Reason string `json:"reason"`
}

func validID(value string) bool {
	if value == "" || len(value) > maxIDLength {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z', character >= '0' && character <= '9', character == '-', character == '_':
		default:
			return false
		}
	}
	return true
}

func validFlagKey(key string) bool {
	if key == "" || len(key) > maxIDLength {
		return false
	}
	for index := 0; index < len(key); index++ {
		character := key[index]
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9', character == '.', character == '_', character == '-':
		default:
			return false
		}
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !validID(id) {
		writeError(w, http.StatusNotFound, "not_found", "That record does not exist.")
		return "", false
	}
	return id, true
}

func actorOf(session selfhost.OwnerSession) selfhost.Actor {
	return selfhost.Actor{Kind: selfhost.ActorOwner, ID: session.Owner.ID}
}

func holderOf(holder selfhost.Holder) holderView {
	return holderView{Kind: string(holder.Kind), ID: holder.ID, Name: holder.Name}
}

func ownerOf(owner selfhost.Owner, current string) ownerRecord {
	return ownerRecord{ID: owner.ID, Name: owner.Name, CreatedAt: stamp(owner.CreatedAt), LastLoginAt: stampPointer(owner.LastLoginAt), SetupPending: owner.SetupPending, Current: owner.ID == current}
}

func memberOf(member selfhost.Member) memberRecord {
	return memberRecord{ID: member.ID, Name: member.Name, CreatedAt: stamp(member.CreatedAt), DeviceCount: member.DeviceCount}
}

func pairingOf(pairing selfhost.Pairing) pairingRecord {
	return pairingRecord{ID: pairing.ID, Holder: holderOf(pairing.Holder), DeviceName: pairing.DeviceNameHint, CreatedBy: pairing.CreatedBy, CreatedAt: stamp(pairing.CreatedAt), ExpiresAt: stamp(pairing.ExpiresAt)}
}

func deviceOf(device selfhost.Device) deviceRecord {
	return deviceRecord{
		ID: device.ID, Name: device.Name, Holder: holderOf(device.Holder),
		AppVersion: device.Meta.AppVersion, Platform: device.Meta.Platform, Architecture: device.Meta.Architecture,
		UpdateChannel: device.Meta.UpdateChannel, ProtocolVersion: device.Meta.ProtocolVersion,
		BrowserHelperCapable: device.Meta.BrowserHelperCapable, BrowserHelperLastObservedAt: stampPointer(device.BrowserHelperLastObservedAt),
		CreatedAt: stamp(device.CreatedAt), ExpiresAt: stamp(device.ExpiresAt), LastSeenAt: stamp(device.LastSeenAt), RevokedAt: stampPointer(device.RevokedAt),
	}
}

func auditOf(entry selfhost.AuditEntry) auditRecord {
	detail := entry.Detail
	if detail == nil {
		detail = map[string]string{}
	}
	return auditRecord{Seq: entry.Seq, Actor: entry.Actor, Action: entry.Action, Target: entry.Target, Detail: detail, At: stamp(entry.At), Hash: entry.Hash}
}

func chainOf(report selfhost.AuditReport) chainRecord {
	chain := chainRecord{OK: report.OK, Rows: report.Rows, HeadSeq: report.HeadSeq, HeadHash: report.HeadHash}
	if report.FirstBreak != nil {
		chain.FirstBreak = &breakEntry{Seq: report.FirstBreak.Seq, Reason: report.FirstBreak.Reason}
	}
	return chain
}

func (s *server) setupLink(token string) string {
	return s.cfg.PublicURL.Origin + "/setup#token=" + url.QueryEscape(token)
}

func (s *server) pairingLink(code string) string {
	return s.cfg.PublicURL.Origin + "/pair#code=" + url.QueryEscape(code) + "&fp=" + s.fingerprint
}

func (s *server) listOwners(w http.ResponseWriter, r *http.Request, session selfhost.OwnerSession) {
	owners, err := s.cfg.Store.ListOwners(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	records := make([]ownerRecord, 0, len(owners))
	for _, owner := range owners {
		records = append(records, ownerOf(owner, session.Owner.ID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"owners": records})
}

func (s *server) inviteOwner(w http.ResponseWriter, r *http.Request, session selfhost.OwnerSession) {
	var input nameRequest
	if !s.decode(w, r, &input, "invalid_request", "The owner details could not be read.") {
		return
	}
	name, ok := selfhost.NormalizeName(input.Name)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_name", "Use a name of 1 to 64 characters with no control characters.")
		return
	}
	issue, err := s.cfg.Store.InviteOwner(r.Context(), actorOf(session), name)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"owner":      ownerOf(issue.Owner, session.Owner.ID),
		"setupToken": issue.Token,
		"link":       s.setupLink(issue.Token),
		"expiresAt":  stamp(issue.ExpiresAt),
	})
}

func (s *server) removeOwner(w http.ResponseWriter, r *http.Request, session selfhost.OwnerSession) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.cfg.Store.RemoveOwner(r.Context(), actorOf(session), id); err != nil {
		s.storeError(w, r, err)
		return
	}
	if id == session.Owner.ID {
		s.clearSessionCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listMembers(w http.ResponseWriter, r *http.Request, _ selfhost.OwnerSession) {
	members, err := s.cfg.Store.ListMembers(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	records := make([]memberRecord, 0, len(members))
	for _, member := range members {
		records = append(records, memberOf(member))
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": records})
}

func (s *server) createMember(w http.ResponseWriter, r *http.Request, session selfhost.OwnerSession) {
	var input nameRequest
	if !s.decode(w, r, &input, "invalid_request", "The member details could not be read.") {
		return
	}
	name, ok := selfhost.NormalizeName(input.Name)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_name", "Use a name of 1 to 64 characters with no control characters.")
		return
	}
	member, err := s.cfg.Store.CreateMember(r.Context(), actorOf(session), name)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"member": memberOf(member)})
}

func (s *server) renameMember(w http.ResponseWriter, r *http.Request, session selfhost.OwnerSession) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input nameRequest
	if !s.decode(w, r, &input, "invalid_request", "The member details could not be read.") {
		return
	}
	name, valid := selfhost.NormalizeName(input.Name)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_name", "Use a name of 1 to 64 characters with no control characters.")
		return
	}
	member, err := s.cfg.Store.RenameMember(r.Context(), actorOf(session), id, name)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"member": memberOf(member)})
}

func (s *server) deleteMember(w http.ResponseWriter, r *http.Request, session selfhost.OwnerSession) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	revoked, err := s.cfg.Store.DeleteMember(r.Context(), actorOf(session), id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revokedDevices": revoked})
}

func (s *server) createPairing(w http.ResponseWriter, r *http.Request, session selfhost.OwnerSession) {
	var input pairingRequest
	if !s.decode(w, r, &input, "invalid_request", "The pairing details could not be read.") {
		return
	}
	hasMember := input.MemberID != ""
	if hasMember == input.Self || (hasMember && !validID(input.MemberID)) {
		writeError(w, http.StatusBadRequest, "invalid_pairing", "Send either memberId or self set to true.")
		return
	}
	hint := strings.TrimSpace(input.DeviceName)
	if hint != "" {
		normalized, ok := selfhost.NormalizeDeviceName(hint)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_pairing", "The device name must be 1 to 64 printable ASCII characters.")
			return
		}
		hint = normalized
	}
	holder := selfhost.Holder{Kind: selfhost.HolderOwner, ID: session.Owner.ID, Name: session.Owner.Name}
	if input.MemberID != "" {
		if !session.RecentAuth(s.now()) {
			writeError(w, http.StatusForbidden, "step_up_required", "Confirm your password and a current code to continue.")
			return
		}
		members, err := s.cfg.Store.ListMembers(r.Context())
		if err != nil {
			s.storeError(w, r, err)
			return
		}
		found := false
		for _, member := range members {
			if member.ID == input.MemberID {
				holder = selfhost.Holder{Kind: selfhost.HolderMember, ID: member.ID, Name: member.Name}
				found = true
				break
			}
		}
		if !found {
			writeError(w, http.StatusNotFound, "not_found", "That record does not exist.")
			return
		}
	}
	issued, err := s.cfg.Store.CreatePairing(r.Context(), actorOf(session), selfhost.PairingInput{Holder: holder, DeviceNameHint: hint})
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"pairingId": issued.Pairing.ID,
		"code":      issued.Code,
		"link":      s.pairingLink(issued.Code),
		"holder":    holderOf(issued.Pairing.Holder),
		"expiresAt": stamp(issued.Pairing.ExpiresAt),
	})
}

func (s *server) listPairings(w http.ResponseWriter, r *http.Request, _ selfhost.OwnerSession) {
	pairings, err := s.cfg.Store.ListPairings(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	records := make([]pairingRecord, 0, len(pairings))
	for _, pairing := range pairings {
		records = append(records, pairingOf(pairing))
	}
	writeJSON(w, http.StatusOK, map[string]any{"pairings": records})
}

func (s *server) cancelPairing(w http.ResponseWriter, r *http.Request, session selfhost.OwnerSession) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.cfg.Store.CancelPairing(r.Context(), actorOf(session), id); err != nil {
		s.storeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listDevices(w http.ResponseWriter, r *http.Request, _ selfhost.OwnerSession) {
	devices, err := s.cfg.Store.ListDevices(r.Context(), selfhost.DeviceFilter{IncludeInactive: r.URL.Query().Get("includeInactive") == "true"})
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	records := make([]deviceRecord, 0, len(devices))
	for _, device := range devices {
		records = append(records, deviceOf(device))
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": records})
}

func (s *server) revokeDevice(w http.ResponseWriter, r *http.Request, session selfhost.OwnerSession) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.cfg.Store.RevokeDevice(r.Context(), actorOf(session), id); err != nil {
		s.storeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) audit(w http.ResponseWriter, r *http.Request, _ selfhost.OwnerSession) {
	full, ok := fullCheckRequested(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	var cursor int64
	if raw := query.Get("cursor"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "The audit cursor is not valid.")
			return
		}
		cursor = parsed
	}
	limit := defaultAuditLimit
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxAuditLimit {
			writeError(w, http.StatusBadRequest, "invalid_limit", "The limit must be from 1 to 200.")
			return
		}
		limit = parsed
	}
	page, err := s.cfg.Store.ListAudit(r.Context(), cursor, limit)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	report, err := s.verifyAudit(r.Context(), full)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	entries := make([]auditRecord, 0, len(page.Entries))
	for _, entry := range page.Entries {
		entries = append(entries, auditOf(entry))
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "nextCursor": page.NextCursor, "chain": chainOf(report)})
}

func (s *server) getSettings(w http.ResponseWriter, r *http.Request, _ selfhost.OwnerSession) {
	instance, err := s.cfg.Store.Instance(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.settingsView(instance))
}

func (s *server) settingsView(instance selfhost.Instance) map[string]any {
	return map[string]any{
		"instanceId":   instance.ID,
		"name":         instance.Name,
		"publicUrl":    s.cfg.PublicURL.Origin,
		"publicUrlSet": instance.PublicURL != "",
		"createdAt":    stamp(instance.CreatedAt),
		"fingerprint":  s.fingerprint,
	}
}

func (s *server) patchSettings(w http.ResponseWriter, r *http.Request, session selfhost.OwnerSession) {
	var input settingsRequest
	if !s.decode(w, r, &input, "invalid_request", "The settings could not be read.") {
		return
	}
	if input.Name == nil && input.PublicURL == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Send a name, a publicUrl or both.")
		return
	}
	var update selfhost.InstanceUpdate
	if input.Name != nil {
		name, ok := selfhost.NormalizeName(*input.Name)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_name", "Use a name of 1 to 64 characters with no control characters.")
			return
		}
		update.Name = &name
	}
	if input.PublicURL != nil {
		normalized, ok := selfhost.NormalizePublicURL(*input.PublicURL)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_public_url", "Use an https address such as https://sesame.example.net. Plain http is accepted only for localhost, 127.0.0.1 and ::1.")
			return
		}
		if normalized != "" && normalized != s.cfg.PublicURL.Origin {
			writeError(w, http.StatusBadRequest, "public_url_mismatch", "This server answers at "+s.cfg.PublicURL.Origin+". Change SESAME_PUBLIC_URL and restart the server to use another address.")
			return
		}
		update.PublicURL = &normalized
	}
	instance, err := s.cfg.Store.UpdateInstance(r.Context(), actorOf(session), update)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.settingsView(instance))
}

func (s *server) listFlags(w http.ResponseWriter, r *http.Request, _ selfhost.OwnerSession) {
	flags, err := s.cfg.Store.Flags(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	records := make([]map[string]any, 0, len(flags))
	for _, flag := range flags {
		records = append(records, map[string]any{"key": flag.Key, "description": flag.Description, "enabled": flag.Enabled, "updatedAt": stamp(flag.UpdatedAt)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"flags": records})
}

func (s *server) patchFlag(w http.ResponseWriter, r *http.Request, session selfhost.OwnerSession) {
	key := r.PathValue("key")
	if !validFlagKey(key) {
		writeError(w, http.StatusNotFound, "not_found", "That record does not exist.")
		return
	}
	var input flagRequest
	if !s.decode(w, r, &input, "invalid_request", "The flag could not be read.") {
		return
	}
	if input.Enabled == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Send enabled as true or false.")
		return
	}
	flag, err := s.cfg.Store.SetFlag(r.Context(), actorOf(session), key, *input.Enabled)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": flag.Key, "description": flag.Description, "enabled": flag.Enabled, "updatedAt": stamp(flag.UpdatedAt)})
}
