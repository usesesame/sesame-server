package ops

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"usesesame.app/backend/internal/selfhost"
)

const ExportFormat = "sesame-selfhost-export-v1"

type exportHolder struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type exportMember struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"createdAt"`
	DeviceCount int       `json:"deviceCount"`
}

type exportDevice struct {
	ID                          string       `json:"id"`
	Name                        string       `json:"name"`
	Holder                      exportHolder `json:"holder"`
	AppVersion                  string       `json:"appVersion"`
	Platform                    string       `json:"platform"`
	Architecture                string       `json:"architecture"`
	UpdateChannel               string       `json:"updateChannel"`
	ProtocolVersion             int          `json:"protocolVersion"`
	BrowserHelperCapable        bool         `json:"browserHelperCapable"`
	BrowserHelperLastObservedAt *time.Time   `json:"browserHelperLastObservedAt"`
	CreatedAt                   time.Time    `json:"createdAt"`
	ExpiresAt                   time.Time    `json:"expiresAt"`
	LastSeenAt                  time.Time    `json:"lastSeenAt"`
	RevokedAt                   *time.Time   `json:"revokedAt"`
}

type exportAudit struct {
	Seq      int64             `json:"seq"`
	Actor    string            `json:"actor"`
	Action   string            `json:"action"`
	Target   string            `json:"target"`
	Detail   map[string]string `json:"detail"`
	At       time.Time         `json:"at"`
	PrevHash string            `json:"prevHash"`
	Hash     string            `json:"hash"`
}

type exportChain struct {
	Version  string `json:"version"`
	OK       bool   `json:"ok"`
	Rows     int64  `json:"rows"`
	HeadHash string `json:"headHash"`
}

type exportDocument struct {
	Format     string         `json:"format"`
	ExportedAt time.Time      `json:"exportedAt"`
	Instance   exportInstance `json:"instance"`
	Members    []exportMember `json:"members"`
	Devices    []exportDevice `json:"devices"`
	AuditChain exportChain    `json:"auditChain"`
	Audit      []exportAudit  `json:"audit"`
}

type exportInstance struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	PublicURL string    `json:"publicUrl"`
	CreatedAt time.Time `json:"createdAt"`
}

func Export(ctx context.Context, store selfhost.Store, w io.Writer, options ...Option) error {
	cfg, err := newConfig(options)
	if err != nil {
		return err
	}
	instance, err := store.Instance(ctx)
	if err != nil {
		return err
	}
	members, err := store.ListMembers(ctx)
	if err != nil {
		return err
	}
	devices, err := store.ListDevices(ctx, selfhost.DeviceFilter{IncludeInactive: true})
	if err != nil {
		return err
	}
	chain, err := store.VerifyAudit(ctx)
	if err != nil {
		return err
	}
	entries, err := collectAudit(ctx, store)
	if err != nil {
		return err
	}
	document := exportDocument{
		Format:     ExportFormat,
		ExportedAt: cfg.now().UTC().Truncate(time.Second),
		Instance:   exportInstance{ID: instance.ID, Name: instance.Name, PublicURL: instance.PublicURL, CreatedAt: instance.CreatedAt},
		Members:    make([]exportMember, 0, len(members)),
		Devices:    make([]exportDevice, 0, len(devices)),
		AuditChain: exportChain{Version: selfhost.AuditChainVersion, OK: chain.OK, Rows: chain.Rows, HeadHash: chain.HeadHash},
		Audit:      entries,
	}
	for _, member := range members {
		document.Members = append(document.Members, exportMember{ID: member.ID, Name: member.Name, CreatedAt: member.CreatedAt, DeviceCount: member.DeviceCount})
	}
	for _, device := range devices {
		document.Devices = append(document.Devices, exportDevice{
			ID: device.ID, Name: device.Name,
			Holder:                      exportHolder{Kind: string(device.Holder.Kind), ID: device.Holder.ID, Name: device.Holder.Name},
			AppVersion:                  device.Meta.AppVersion,
			Platform:                    device.Meta.Platform,
			Architecture:                device.Meta.Architecture,
			UpdateChannel:               device.Meta.UpdateChannel,
			ProtocolVersion:             device.Meta.ProtocolVersion,
			BrowserHelperCapable:        device.Meta.BrowserHelperCapable,
			BrowserHelperLastObservedAt: device.BrowserHelperLastObservedAt,
			CreatedAt:                   device.CreatedAt, ExpiresAt: device.ExpiresAt, LastSeenAt: device.LastSeenAt, RevokedAt: device.RevokedAt,
		})
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(document)
}

func collectAudit(ctx context.Context, store selfhost.AuditStore) ([]exportAudit, error) {
	var newestFirst []exportAudit
	var cursor int64
	for {
		page, err := store.ListAudit(ctx, cursor, 200)
		if err != nil {
			return nil, err
		}
		for _, entry := range page.Entries {
			detail := entry.Detail
			if detail == nil {
				detail = map[string]string{}
			}
			newestFirst = append(newestFirst, exportAudit{Seq: entry.Seq, Actor: entry.Actor, Action: entry.Action, Target: entry.Target, Detail: detail, At: entry.At, PrevHash: entry.PrevHash, Hash: entry.Hash})
		}
		if page.NextCursor == 0 || len(page.Entries) == 0 {
			break
		}
		cursor = page.NextCursor
	}
	ordered := make([]exportAudit, len(newestFirst))
	for index, entry := range newestFirst {
		ordered[len(newestFirst)-1-index] = entry
	}
	return ordered, nil
}
