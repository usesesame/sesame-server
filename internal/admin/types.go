package admin

import "time"

type Role string

const (
	RoleSuper    Role = "super"
	RoleSupport  Role = "support"
	RoleOps      Role = "ops"
	RoleBilling  Role = "billing"
	RoleReadonly Role = "readonly"
)

func ValidRole(role Role) bool {
	switch role {
	case RoleSuper, RoleSupport, RoleOps, RoleBilling, RoleReadonly:
		return true
	default:
		return false
	}
}

type Permission string

const (
	PermissionUsersRead     Permission = "users:read"
	PermissionUsersManage   Permission = "users:manage"
	PermissionUsersDelete   Permission = "users:delete"
	PermissionFlagsManage   Permission = "flags:manage"
	PermissionReleaseWrite  Permission = "releases:write"
	PermissionPlansWrite    Permission = "plans:write"
	PermissionAdminsManage  Permission = "admins:manage"
	PermissionAuditAll      Permission = "audit:all"
	PermissionSystemRead    Permission = "system:read"
	PermissionSupportManage Permission = "support:manage"
	PermissionSupportRead   Permission = "support:read"
)

func Allowed(role Role, permission Permission) bool {
	if role == RoleSuper {
		return true
	}
	switch permission {
	case PermissionUsersRead:
		return role == RoleSupport || role == RoleBilling || role == RoleReadonly
	case PermissionUsersManage:
		return role == RoleSupport
	case PermissionFlagsManage, PermissionReleaseWrite:
		return role == RoleOps
	case PermissionPlansWrite:
		return role == RoleBilling
	case PermissionAuditAll:
		return role == RoleReadonly
	case PermissionSystemRead:
		return role == RoleOps || role == RoleReadonly
	case PermissionSupportManage:
		return role == RoleSupport
	case PermissionSupportRead:
		return role == RoleSupport || role == RoleReadonly
	default:
		return false
	}
}

type Account struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	Role        Role      `json:"role"`
	MFAVerified bool      `json:"mfaVerified"`
	Suspended   bool      `json:"suspended"`
	CreatedAt   time.Time `json:"createdAt"`
	LastLoginAt time.Time `json:"lastLoginAt,omitempty"`
}

type UserSummary struct {
	ID            string     `json:"id"`
	Email         string     `json:"email"`
	EmailVerified bool       `json:"emailVerified"`
	BetaAccess    bool       `json:"betaAccess"`
	SuspendedAt   *time.Time `json:"suspendedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	SessionCount  int        `json:"sessionCount"`
	DeviceCount   int        `json:"deviceCount"`
}

type Session struct {
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

type Device struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	ConnectedAt time.Time `json:"connectedAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type UserDetail struct {
	UserSummary
	SuspendedReason string    `json:"suspendedReason,omitempty"`
	Sessions        []Session `json:"sessions"`
	Devices         []Device  `json:"devices"`
}

type FeatureFlag struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Plan struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Price       string    `json:"price"`
	AnnualPrice string    `json:"annualPrice,omitempty"`
	Billing     string    `json:"billing"`
	Description string    `json:"description"`
	Available   bool      `json:"available"`
	Includes    []string  `json:"includes"`
	UpdatedAt   time.Time `json:"updatedAt,omitempty"`
}

type Release struct {
	ID                string           `json:"id"`
	Channel           string           `json:"channel"`
	Platform          string           `json:"platform"`
	Architecture      string           `json:"architecture"`
	Version           string           `json:"version"`
	URL               string           `json:"url"`
	ArtifactObjectKey string           `json:"-"`
	SHA256            string           `json:"sha256"`
	Signature         string           `json:"signature"`
	SigningKeyID      string           `json:"signingKeyId"`
	SupportedWindows  string           `json:"supportedWindows"`
	ReleaseNotesURL   string           `json:"releaseNotesUrl"`
	RollbackNotice    string           `json:"rollbackNotice"`
	Status            string           `json:"status"`
	RolloutPercent    int              `json:"rolloutPercent"`
	UpdateEnabled     bool             `json:"updateEnabled"`
	KillSwitch        bool             `json:"killSwitch"`
	ManifestRevision  int64            `json:"manifestRevision"`
	PublishedAt       *time.Time       `json:"publishedAt,omitempty"`
	Artifact          *ReleaseArtifact `json:"artifact,omitempty"`
}

// Verification output from the signed release pipeline; read-only, not editable through release controls.
type ReleaseArtifact struct {
	ID                     string         `json:"id"`
	URL                    string         `json:"url"`
	ObjectKey              string         `json:"objectKey"`
	SHA256                 string         `json:"sha256"`
	Bytes                  int64          `json:"bytes"`
	UpdaterSignature       string         `json:"updaterSignature"`
	UpdaterSigningKeyID    string         `json:"updaterSigningKeyId"`
	DistributionClass      string         `json:"distributionClass"`
	SigstoreEvidence       map[string]any `json:"sigstoreEvidence,omitempty"`
	SigstoreVerified       bool           `json:"sigstoreVerified"`
	SigstoreIssuer         string         `json:"sigstoreIssuer,omitempty"`
	SigstoreIdentity       string         `json:"sigstoreIdentity,omitempty"`
	SigstoreBundleSHA256   string         `json:"sigstoreBundleSha256,omitempty"`
	AuthenticodeEvidence   map[string]any `json:"authenticodeEvidence,omitempty"`
	AuthenticodeVerified   bool           `json:"authenticodeVerified"`
	AuthenticodeSubject    string         `json:"authenticodeSubject,omitempty"`
	AuthenticodeThumbprint string         `json:"authenticodeThumbprint,omitempty"`
	VerifiedAt             time.Time      `json:"verifiedAt"`
	CandidatePayload       string         `json:"-"`
	CandidateSigningKeyID  string         `json:"-"`
	CandidateSignature     string         `json:"-"`
}

type ReleaseCandidate struct {
	SchemaVersion         int             `json:"schemaVersion"`
	Version               string          `json:"version"`
	Channel               string          `json:"channel"`
	Platform              string          `json:"platform"`
	Architecture          string          `json:"architecture"`
	SupportedWindows      string          `json:"supportedWindows"`
	ReleaseNotesURL       string          `json:"releaseNotesUrl"`
	Artifact              ReleaseArtifact `json:"artifact"`
	CandidateSigningKeyID string          `json:"candidateSigningKeyId"`
	CandidateSignature    string          `json:"candidateSignature"`
	SigningPayload        string          `json:"-"`
}

type AuditEntry struct {
	ID         int64          `json:"id"`
	AdminID    *string        `json:"adminId,omitempty"`
	AdminEmail string         `json:"adminEmail"`
	Action     string         `json:"action"`
	TargetType string         `json:"targetType"`
	TargetID   *string        `json:"targetId,omitempty"`
	Detail     map[string]any `json:"detail"`
	CreatedAt  time.Time      `json:"createdAt"`
}

type AuditFilter struct {
	AdminID string
	Action  string
	From    time.Time
	To      time.Time
}

type Overview struct {
	Users               int `json:"users"`
	NewUsersThisWeek    int `json:"newUsersThisWeek"`
	BetaUsers           int `json:"betaUsers"`
	UnverifiedUsers     int `json:"unverifiedUsers"`
	SuspendedUsers      int `json:"suspendedUsers"`
	ActiveAdminSessions int `json:"activeAdminSessions"`
	OpenTickets         int `json:"openTickets"`
	UnassignedTickets   int `json:"unassignedTickets"`
	UrgentTickets       int `json:"urgentTickets"`
}

type RateLimitMetric struct {
	Operation string    `json:"operation"`
	Buckets   int       `json:"buckets"`
	Attempts  int       `json:"attempts"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type TicketStatus string

const (
	TicketOpen       TicketStatus = "open"
	TicketInProgress TicketStatus = "in_progress"
	TicketWaiting    TicketStatus = "waiting"
	TicketClosed     TicketStatus = "closed"
)

func ValidTicketStatus(s string) bool {
	switch TicketStatus(s) {
	case TicketOpen, TicketInProgress, TicketWaiting, TicketClosed:
		return true
	default:
		return false
	}
}

type TicketPriority string

const (
	PriorityLow    TicketPriority = "low"
	PriorityNormal TicketPriority = "normal"
	PriorityHigh   TicketPriority = "high"
	PriorityUrgent TicketPriority = "urgent"
)

func ValidTicketPriority(p string) bool {
	switch TicketPriority(p) {
	case PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent:
		return true
	default:
		return false
	}
}

type TicketCategory string

const (
	CategoryGeneral       TicketCategory = "general"
	CategoryAccount       TicketCategory = "account"
	CategoryImport        TicketCategory = "import"
	CategorySync          TicketCategory = "sync"
	CategoryBrowserHelper TicketCategory = "browser_helper"
	CategoryBilling       TicketCategory = "billing"
	CategoryBug           TicketCategory = "bug"
)

var TicketCategories = []TicketCategory{
	CategoryGeneral, CategoryAccount, CategoryImport, CategorySync, CategoryBrowserHelper, CategoryBilling, CategoryBug,
}

func ValidTicketCategory(c string) bool {
	switch TicketCategory(c) {
	case CategoryGeneral, CategoryAccount, CategoryImport, CategorySync, CategoryBrowserHelper, CategoryBilling, CategoryBug:
		return true
	default:
		return false
	}
}

type TicketSummary struct {
	ID                 string         `json:"id"`
	Email              string         `json:"email"`
	Subject            string         `json:"subject"`
	Status             TicketStatus   `json:"status"`
	Priority           TicketPriority `json:"priority"`
	Category           TicketCategory `json:"category"`
	AppVersion         string         `json:"appVersion"`
	DiagnosticCode     string         `json:"diagnosticCode"`
	BrowserIntegration string         `json:"browserIntegration"`
	RequestID          string         `json:"requestId"`
	AssignedAdminID    *string        `json:"assignedAdminId,omitempty"`
	MessageCount       int            `json:"messageCount"`
	CreatedAt          time.Time      `json:"createdAt"`
	UpdatedAt          time.Time      `json:"updatedAt"`
	FirstResponseAt    *time.Time     `json:"firstResponseAt,omitempty"`
	ClosedAt           *time.Time     `json:"closedAt,omitempty"`
	SLADueAt           time.Time      `json:"slaDueAt"`
	SLABreached        bool           `json:"slaBreached"`
	QueuePosition      int            `json:"queuePosition,omitempty"`
}

type TicketMessage struct {
	ID                  string     `json:"id"`
	AuthorRole          string     `json:"authorRole"`
	AdminEmail          string     `json:"adminEmail,omitempty"`
	Body                string     `json:"body"`
	SentViaEmail        bool       `json:"sentViaEmail"`
	EmailDeliveryStatus string     `json:"emailDeliveryStatus,omitempty"`
	EmailAttempts       int        `json:"emailAttempts,omitempty"`
	EmailNextAttemptAt  *time.Time `json:"emailNextAttemptAt,omitempty"`
	CreatedAt           time.Time  `json:"createdAt"`
}

type TicketNote struct {
	ID         string    `json:"id"`
	AdminEmail string    `json:"adminEmail"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"createdAt"`
}

type TicketDetail struct {
	TicketSummary
	AccountID     string          `json:"accountId,omitempty"`
	LinkedDevices []Device        `json:"linkedDevices,omitempty"`
	Messages      []TicketMessage `json:"messages"`
	Notes         []TicketNote    `json:"notes"`
}

type TicketListFilter struct {
	Status   string
	Priority string
	Category string
	Assigned string
	Query    string
}

var VaultShapedFields = map[string]struct{}{
	"vault": {}, "vaultData": {}, "vaultId": {}, "vaultPassword": {}, "masterPassword": {},
	"passwords": {}, "totpSecret": {}, "totpSeed": {}, "backupCodes": {}, "recoveryKit": {},
	"recoveryNotes": {}, "credentialImport": {}, "vaultKey": {}, "ciphertext": {},
}
