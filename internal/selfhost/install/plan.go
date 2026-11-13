package install

type Options struct {
	Profile     string
	Version     string
	Host        string
	Directory   string
	ToolVersion string
}

type Plan struct {
	SchemaVersion  int            `json:"schemaVersion"`
	ToolVersion    string         `json:"toolVersion"`
	Profile        string         `json:"profile"`
	ImageVersion   string         `json:"imageVersion"`
	PublicURL      string         `json:"publicUrl"`
	TrustedProxies []string       `json:"trustedProxies"`
	Files          []File         `json:"files"`
	Prerequisites  []Prerequisite `json:"prerequisites"`
}

type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode"`
}

type Prerequisite struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Message string `json:"message"`
}
