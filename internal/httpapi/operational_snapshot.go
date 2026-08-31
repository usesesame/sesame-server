package httpapi

import (
	"context"
	"errors"
)

type operationalSnapshotProvider struct {
	config Config
}

func newOperationalSnapshotProvider(config Config) operationalSnapshotProvider {
	return operationalSnapshotProvider{config: config}
}

func (p operationalSnapshotProvider) Snapshot(ctx context.Context) OperationalSnapshot {
	snapshot := OperationalSnapshot{
		API:              OperationalComponent{Status: OperationalReady},
		Version:          OperationalVersion{Version: p.config.Version, Commit: p.config.Commit},
		Schema:           OperationalSchema{Status: OperationalUnavailable},
		Database:         OperationalDatabase{Status: OperationalUnavailable},
		ReleasePipeline:  OperationalComponent{Status: operationalReleasePipelineStatus(p.config)},
		ArtifactDelivery: OperationalComponent{Status: operationalArtifactDeliveryStatus(p.config)},
		EmailOutbox:      OperationalEmailOutbox{Status: OperationalNotConfigured},
		Maintenance:      OperationalMaintenance{Status: OperationalNotRun},
	}
	if p.config.Admin != nil {
		if err := p.config.Admin.Ping(ctx); err == nil {
			snapshot.Database.Status = OperationalReady
		} else if errors.Is(err, context.DeadlineExceeded) {
			snapshot.Database.TimedOut = true
		}
		if version, err := p.config.Admin.SchemaVersion(ctx); err == nil && version != "" {
			snapshot.Schema = OperationalSchema{Status: OperationalReady, Version: version}
		}
	}
	if p.config.OperationalOutbox != nil {
		if summary, err := p.config.OperationalOutbox.OperationalSummary(ctx); err == nil {
			snapshot.EmailOutbox = boundedOutboxSummary(summary)
		} else {
			snapshot.EmailOutbox.Status = OperationalUnavailable
		}
	}
	if p.config.Maintenance != nil {
		snapshot.Maintenance = p.config.Maintenance.Snapshot()
	}
	return snapshot
}

func boundedOutboxSummary(summary OperationalEmailOutbox) OperationalEmailOutbox {
	summary.Pending = min(max(summary.Pending, 0), 100)
	summary.Failed = min(max(summary.Failed, 0), 100)
	return summary
}

func operationalReleasePipelineStatus(config Config) OperationalStatus {
	if len(config.ReleaseCandidatePublicKey) == 0 || len(config.ReleaseCandidateTokenHash) == 0 || config.ReleaseCandidateKeyID == "" {
		return OperationalNotConfigured
	}
	return OperationalReady
}

func operationalArtifactDeliveryStatus(config Config) OperationalStatus {
	if config.ArtifactDelivery == nil {
		return OperationalNotConfigured
	}
	return OperationalReady
}
