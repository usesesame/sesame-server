package selfhost

import "time"

const (
	Profile                  = "selfhost"
	AuditChainVersion        = "sesame-selfhost-audit-v1"
	PairingTTL               = 10 * time.Minute
	PairingCodeMinLength     = 32
	PairingCodeMaxLength     = 128
	DeviceTokenTTL           = 90 * 24 * time.Hour
	SetupTokenTTL            = 24 * time.Hour
	RecentAuthWindow         = 10 * time.Minute
	DefaultSessionTTL        = 12 * time.Hour
	MinPasswordLength        = 12
	MaxPasswordLength        = 1024
	MaxNameLength            = 64
	MaxMetaLength            = 64
	MaxAuditDetailEntries    = 16
	MaxAuditDetailValueBytes = 512
)

type ActorKind string

const (
	ActorOwner  ActorKind = "owner"
	ActorDevice ActorKind = "device"
	ActorSystem ActorKind = "system"
)

type Actor struct {
	Kind ActorKind
	ID   string
}

func (a Actor) String() string {
	if a.ID == "" {
		return string(a.Kind)
	}
	return string(a.Kind) + ":" + a.ID
}

var SystemActor = Actor{Kind: ActorSystem}

type HolderKind string

const (
	HolderOwner  HolderKind = "owner"
	HolderMember HolderKind = "member"
)

type Holder struct {
	Kind HolderKind
	ID   string
	Name string
}

type Instance struct {
	ID        string
	Name      string
	PublicURL string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type InstanceUpdate struct {
	Name      *string
	PublicURL *string
}

type Owner struct {
	ID           string
	Name         string
	CreatedAt    time.Time
	LastLoginAt  *time.Time
	SetupPending bool
}

type SetupIssue struct {
	Owner     Owner
	Token     string
	ExpiresAt time.Time
}

type SetupDetails struct {
	TOTPSecret string
	OwnerName  string
	FirstOwner bool
	ExpiresAt  time.Time
}

type CompleteSetupInput struct {
	Token     string
	Name      string
	Password  string
	Code      string
	UserAgent string

	UpdateChecks *bool
}

type LoginInput struct {
	Name      string
	Password  string
	Code      string
	UserAgent string
}

type StepUpInput struct {
	SessionToken string
	Password     string
	Code         string
}

type OwnerSession struct {
	ID           string
	Owner        Owner
	CreatedAt    time.Time
	ExpiresAt    time.Time
	RecentAuthAt time.Time
	CSRFToken    string
}

func (s OwnerSession) RecentAuthUntil() time.Time {
	return s.RecentAuthAt.Add(RecentAuthWindow)
}

func (s OwnerSession) RecentAuth(now time.Time) bool {
	return now.Before(s.RecentAuthUntil())
}

type OwnerLogin struct {
	Token   string
	Session OwnerSession
}

type Member struct {
	ID          string
	Name        string
	CreatedAt   time.Time
	DeviceCount int
}

type PairingInput struct {
	Holder         Holder
	DeviceNameHint string
}

type Pairing struct {
	ID             string
	Holder         Holder
	DeviceNameHint string
	CreatedBy      string
	CreatedAt      time.Time
	ExpiresAt      time.Time
}

type IssuedPairing struct {
	Pairing Pairing
	Code    string
}

type DeviceMeta struct {
	AppVersion            string
	Platform              string
	Architecture          string
	UpdateChannel         string
	ProtocolVersion       int
	BrowserHelperCapable  bool
	BrowserHelperObserved bool
}

type RedeemInput struct {
	Code       string
	DeviceName string
	Meta       DeviceMeta
}

type Device struct {
	ID                          string
	Name                        string
	Holder                      Holder
	Meta                        DeviceMeta
	CreatedAt                   time.Time
	ExpiresAt                   time.Time
	LastSeenAt                  time.Time
	RevokedAt                   *time.Time
	BrowserHelperLastObservedAt *time.Time
}

type IssuedDevice struct {
	Device Device
	Token  string
}

type DeviceFilter struct {
	IncludeInactive bool
}

type FlagDefinition struct {
	Key         string
	Description string
	Default     bool
}

type Flag struct {
	Key         string
	Description string
	Enabled     bool
	UpdatedAt   time.Time
}

type AuditInput struct {
	Actor  Actor
	Action string
	Target string
	Detail map[string]string
}

type AuditEntry struct {
	Seq      int64
	Actor    string
	Action   string
	Target   string
	Detail   map[string]string
	At       time.Time
	PrevHash string
	Hash     string
}

type AuditPage struct {
	Entries    []AuditEntry
	NextCursor int64
}

type AuditBreak struct {
	Seq    int64
	Reason string
}

type AuditReport struct {
	OK         bool
	Rows       int64
	HeadSeq    int64
	HeadHash   string
	FirstBreak *AuditBreak
}

type RateDecision struct {
	Allowed    bool
	Remaining  int
	RetryAfter time.Duration
}

type MaintenanceReport struct {
	Sessions    int64
	SetupTokens int64
	Pairings    int64
	Devices     int64
	RateLimits  int64
	Holders     int64
}

type SystemInfo struct {
	SchemaVersion  int
	DatabaseBytes  int64
	LastBackupAt   *time.Time
	ActiveOwners   int
	Members        int
	ActiveDevices  int
	PendingPairing int
}

type CheckReport struct {
	SchemaVersion     int
	IntegrityProblems []string
	ForeignKeyProblem int
	Audit             AuditReport
}

func (r CheckReport) OK() bool {
	return len(r.IntegrityProblems) == 0 && r.ForeignKeyProblem == 0 && r.Audit.OK
}
