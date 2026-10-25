package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/syncsvc"
)

const syncProtocolVersion = 3

type syncLimit struct {
	perIP, perDevice int
}

var syncLimits = map[string]syncLimit{
	"state":           {200, 30},
	"vault":           {30, 5},
	"device-list":     {200, 30},
	"devices":         {200, 30},
	"pairing-begin":   {30, 5},
	"pairing-mine":    {200, 60},
	"pairing-answer":  {60, 10},
	"pairing-confirm": {60, 10},
	"pairing-cancel":  {60, 10},
	"approve":         {60, 10},
	"remove":          {60, 10},
	"rotate":          {60, 10},
	"device-key":      {60, 10},
	"key-package":     {90, 15},
	"activate":        {60, 10},
	"upload":          {180, 30},
	"headers":         {300, 60},
	"download":        {300, 60},
	"checkpoint-put":  {60, 10},
	"checkpoints":     {200, 30},
	"reset":           {10, 3},
}

func (s *server) syncEnabled(ctx context.Context) bool {
	if s.cfg.Sync == nil {
		return false
	}
	flags, err := s.cfg.Store.Flags(ctx)
	if err != nil {
		return false
	}
	for _, flag := range flags {
		if flag.Key == FlagSync {
			return flag.Enabled
		}
	}
	return false
}

func syncOwnerKey(holder selfhost.Holder) string {
	return string(holder.Kind) + ":" + holder.ID
}

type syncHost struct {
	server *server
}

func (host syncHost) Authenticate(w http.ResponseWriter, r *http.Request, operation string) (syncsvc.Caller, bool) {
	s := host.server
	limit, known := syncLimits[operation]
	if !known {
		writeError(w, http.StatusServiceUnavailable, "sync_unavailable", "Sync is temporarily unavailable.")
		return syncsvc.Caller{}, false
	}
	if !s.hostGuard(w, r) {
		return syncsvc.Caller{}, false
	}
	if isUnsafeMethod(r.Method) && r.Header.Get("Origin") != "" {
		writeError(w, http.StatusForbidden, "origin_not_allowed", "Sync is not available from a browser.")
		return syncsvc.Caller{}, false
	}
	if !s.syncEnabled(r.Context()) {
		writeError(w, http.StatusForbidden, "sync_unavailable", "Sync is not turned on for this server.")
		return syncsvc.Caller{}, false
	}
	if !s.allowIP(w, r, "sync-"+operation, limit.perIP, time.Minute) {
		return syncsvc.Caller{}, false
	}
	device, token, ok := s.authenticateDevice(w, r)
	if !ok {
		return syncsvc.Caller{}, false
	}
	if !s.allowKey(w, r, "sync-"+operation+":device:"+s.digest("device", device.ID), limit.perDevice, time.Minute) {
		return syncsvc.Caller{}, false
	}
	return syncsvc.Caller{OwnerKey: syncOwnerKey(device.Holder), TokenDeviceID: device.ID, Token: token}, true
}

type syncOverviewRecord struct {
	Kind            string     `json:"kind"`
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	HasVault        bool       `json:"hasVault"`
	Devices         int        `json:"devices"`
	CurrentRevision uint64     `json:"currentRevision"`
	StoredBytes     int64      `json:"storedBytes"`
	UpdatedAt       *time.Time `json:"updatedAt,omitempty"`
}

func (s *server) syncOverview(w http.ResponseWriter, r *http.Request, _ selfhost.OwnerSession) {
	enabled := s.syncEnabled(r.Context())
	if s.cfg.Sync == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "enabled": false, "holders": []syncOverviewRecord{}})
		return
	}
	holders, err := s.syncHolders(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	records := make([]syncOverviewRecord, 0, len(holders))
	for _, holder := range holders {
		overview, err := s.cfg.Sync.Overview(r.Context(), syncOwnerKey(holder))
		if err != nil {
			requestLog(r.Context()).Error("Sesame server could not summarise Sync", "error", err)
			writeError(w, http.StatusServiceUnavailable, "sync_unavailable", "Sync details are temporarily unavailable.")
			return
		}
		record := syncOverviewRecord{
			Kind: string(holder.Kind), ID: holder.ID, Name: holder.Name, HasVault: overview.HasVault,
			Devices: overview.Devices, CurrentRevision: overview.CurrentRevision, StoredBytes: overview.StoredBytes,
		}
		if overview.HasVault {
			updated := overview.UpdatedAt.UTC()
			record.UpdatedAt = &updated
		}
		records = append(records, record)
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "enabled": enabled, "holders": records})
}

func (s *server) syncHolders(ctx context.Context) ([]selfhost.Holder, error) {
	owners, err := s.cfg.Store.ListOwners(ctx)
	if err != nil {
		return nil, err
	}
	members, err := s.cfg.Store.ListMembers(ctx)
	if err != nil {
		return nil, err
	}
	holders := make([]selfhost.Holder, 0, len(owners)+len(members))
	for _, owner := range owners {
		holders = append(holders, selfhost.Holder{Kind: selfhost.HolderOwner, ID: owner.ID, Name: owner.Name})
	}
	for _, member := range members {
		holders = append(holders, selfhost.Holder{Kind: selfhost.HolderMember, ID: member.ID, Name: member.Name})
	}
	return holders, nil
}

func (s *server) syncReset(w http.ResponseWriter, r *http.Request, session selfhost.OwnerSession) {
	if s.cfg.Sync == nil {
		writeError(w, http.StatusNotFound, "not_found", "That record does not exist.")
		return
	}
	kind, id := r.PathValue("kind"), r.PathValue("id")
	if (kind != string(selfhost.HolderOwner) && kind != string(selfhost.HolderMember)) || id == "" || len(id) > 128 {
		writeError(w, http.StatusNotFound, "not_found", "That record does not exist.")
		return
	}
	holders, err := s.syncHolders(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	known := false
	for _, holder := range holders {
		known = known || (string(holder.Kind) == kind && holder.ID == id)
	}
	if !known {
		writeError(w, http.StatusNotFound, "not_found", "That record does not exist.")
		return
	}
	if err := s.cfg.Sync.AdminReset(r.Context(), kind+":"+id); err != nil {
		if errors.Is(err, syncsvc.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "That person has no synced vault.")
			return
		}
		requestLog(r.Context()).Error("Sesame server could not reset a synced vault", "error", err)
		writeError(w, http.StatusServiceUnavailable, "sync_unavailable", "The synced vault could not be reset.")
		return
	}
	if _, err := s.cfg.Store.AppendAudit(r.Context(), selfhost.AuditInput{
		Actor: actorOf(session), Action: "sync.reset", Target: kind + ":" + id,
	}); err != nil {
		requestLog(r.Context()).Error("Sesame server could not record a Sync reset", "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}
