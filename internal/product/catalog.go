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
			ID: "free", Name: "Sesame", Price: "0", Billing: "none", Available: true,
			Description: "The whole app, free and open source under the AGPL.",
			Includes:    []string{"Encrypted vault", "15 import formats", "Nine record types", "2FA and recovery details", "Windows Hello and PIN unlock", "Backup, restore, and export"},
		},
		{
			ID: "sync", Name: "Sesame Sync", Price: "1.00", AnnualPrice: "10.00", Billing: "monthly", Available: false,
			Description: "Optional hosted sync between your own approved devices. Not available until its security review passes.",
			Includes:    []string{"Approved devices", "End-to-end encryption", "Conflict review", "Local access if Sync ends", "Self-host it instead if you prefer"},
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
