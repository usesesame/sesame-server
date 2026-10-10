package selfhost

import (
	"context"
	"time"
)

type InstanceStore interface {
	Instance(ctx context.Context) (Instance, error)
	UpdateInstance(ctx context.Context, by Actor, update InstanceUpdate) (Instance, error)
	SetupRequired(ctx context.Context) (bool, error)
}

type OwnerStore interface {
	StartFirstSetup(ctx context.Context) (SetupIssue, error)
	InviteOwner(ctx context.Context, by Actor, name string) (SetupIssue, error)
	ResetOwner(ctx context.Context, by Actor, name string) (SetupIssue, error)
	SetupDetails(ctx context.Context, token string) (SetupDetails, error)
	CompleteSetup(ctx context.Context, input CompleteSetupInput) (OwnerLogin, error)
	Login(ctx context.Context, input LoginInput) (OwnerLogin, error)
	Session(ctx context.Context, token string) (OwnerSession, error)
	StepUp(ctx context.Context, input StepUpInput) (OwnerSession, error)
	Logout(ctx context.Context, token string) error
	ListOwners(ctx context.Context) ([]Owner, error)
	RemoveOwner(ctx context.Context, by Actor, id string) error
}

type MemberStore interface {
	CreateMember(ctx context.Context, by Actor, name string) (Member, error)
	ListMembers(ctx context.Context) ([]Member, error)
	RenameMember(ctx context.Context, by Actor, id, name string) (Member, error)
	DeleteMember(ctx context.Context, by Actor, id string) (revokedDevices int, err error)
}

type PairingStore interface {
	CreatePairing(ctx context.Context, by Actor, input PairingInput) (IssuedPairing, error)
	ListPairings(ctx context.Context) ([]Pairing, error)
	CancelPairing(ctx context.Context, by Actor, id string) error
	RedeemPairing(ctx context.Context, input RedeemInput) (IssuedDevice, error)
}

type DeviceStore interface {
	AuthenticateDevice(ctx context.Context, token string) (Device, error)
	Heartbeat(ctx context.Context, token string, meta DeviceMeta) (Device, error)
	Disconnect(ctx context.Context, token string) error
	ListDevices(ctx context.Context, filter DeviceFilter) ([]Device, error)
	RevokeDevice(ctx context.Context, by Actor, id string) error
	RevokeMemberDevices(ctx context.Context, by Actor, memberID string) (int, error)
}

type FlagStore interface {
	Flags(ctx context.Context) ([]Flag, error)
	SetFlag(ctx context.Context, by Actor, key string, enabled bool) (Flag, error)
}

type AuditStore interface {
	AppendAudit(ctx context.Context, input AuditInput) (AuditEntry, error)
	ListAudit(ctx context.Context, cursor int64, limit int) (AuditPage, error)
	VerifyAudit(ctx context.Context) (AuditReport, error)
	VerifyAuditIncremental(ctx context.Context) (AuditReport, error)
}

type RateLimitStore interface {
	RateLimit(ctx context.Context, key string, limit int, window time.Duration) (RateDecision, error)
	ResetRateLimit(ctx context.Context, key string) error
}

type OperationsStore interface {
	Maintain(ctx context.Context) (MaintenanceReport, error)
	System(ctx context.Context) (SystemInfo, error)
	Check(ctx context.Context) (CheckReport, error)
	Backup(ctx context.Context, destination string) error
	RecordBackup(ctx context.Context, at time.Time) error
}

type Store interface {
	InstanceStore
	OwnerStore
	MemberStore
	PairingStore
	DeviceStore
	FlagStore
	AuditStore
	RateLimitStore
	OperationsStore
	UpdateStore
	Ping(ctx context.Context) error
	Close() error
}
