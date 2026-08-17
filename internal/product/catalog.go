package product

type Plan struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Price       string   `json:"price"`
	AnnualPrice string   `json:"annualPrice,omitempty"`
	Billing     string   `json:"billing"`
	Available   bool     `json:"available"`
	Description string   `json:"description"`
	Includes    []string `json:"includes"`
}

func Plans() []Plan {
	return []Plan{
		{
			ID: "free", Name: "Local Vault", Price: "0", Billing: "none", Available: true,
			Description: "Your everyday password vault, with no subscription.",
			Includes:    []string{"Encrypted vault", "15 import formats", "Nine record types", "2FA and recovery details", "Backup, restore, and export"},
		},
		{
			ID: "founding-pro", Name: "Founding Pro", Price: "20.00", Billing: "one_time", Available: false,
			Description: "Pay once for the first set of Pro desktop tools.",
			Includes:    []string{"Multiple vault profiles", "Bulk cleanup tools", "Backup health checks", "All Pro updates in Sesame 1.x", "12 months of Sync if it launches"},
		},
		{
			ID: "sync", Name: "Sesame Sync", Price: "2.50", AnnualPrice: "24.00", Billing: "monthly", Available: false,
			Description: "Optional encrypted Sync after independent review.",
			Includes:    []string{"Approved devices", "End-to-end encryption", "Conflict review", "Local access if Sync ends"},
		},
	}
}

type Release struct {
	Channel   string `json:"channel"`
	Platform  string `json:"platform"`
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	URL       string `json:"url,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Signed    bool   `json:"signed"`
	Message   string `json:"message"`
}

func LatestWindowsRelease() Release {
	return Release{
		Channel: "private-beta", Platform: "windows", Available: false, Signed: false,
		Message: "A public Windows download will appear only after signing and beta verification are complete.",
	}
}
