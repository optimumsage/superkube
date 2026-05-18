package helm

// Release matches the shape emitted by `helm list -o json`. Helm uses snake-ish
// JSON keys; we tolerate unknown fields.
type Release struct {
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
	Revision   string `json:"revision"` // helm returns this as string in list output
	Updated    string `json:"updated"`
	Status     string `json:"status"`
	Chart      string `json:"chart"`
	AppVersion string `json:"app_version"`
}

// Revision is one row from `helm history -o json`.
type Revision struct {
	Revision    int    `json:"revision"`
	Updated     string `json:"updated"`
	Status      string `json:"status"`
	Chart       string `json:"chart"`
	AppVersion  string `json:"app_version"`
	Description string `json:"description"`
}

// Repo is one row from `helm repo list -o json`.
type Repo struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// SearchHit is one row from `helm search repo -o json`.
type SearchHit struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	AppVersion  string `json:"app_version"`
	Description string `json:"description"`
}

// ReleaseRef identifies a Helm release by namespace + name. Returned by
// DetectReleases so the UI can render a "releases exist" hint even when the
// helm binary is not installed.
type ReleaseRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Revision  int    `json:"revision"`
}
