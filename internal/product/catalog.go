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
			Description: "The whole app, open source under the AGPL. There is no paid version.",
			Includes:    []string{"Encrypted local vault", "Import from 15 formats", "Nine item types", "2FA codes and recovery details", "Windows Hello and PIN unlock", "Backup, restore, and export", "Sync with your own server"},
		},
		{
			ID: "sync", Name: "Sesame Sync", Price: "1.00", AnnualPrice: "10.00", Billing: "monthly", Available: false,
			Description: "Keeps your vault the same on every device you approve. We run the server, so you do not have to.",
			Includes:    []string{"Sync server run by us", "End-to-end encryption", "Device approval", "Conflict review", "Local access if you cancel"},
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

func LatestLinuxRelease() Release {
	return Release{
		Channel: "private-beta", Platform: "linux", Available: false, Signed: false,
		Message: "A public Linux download will appear only after signing and beta verification are complete.",
	}
}
