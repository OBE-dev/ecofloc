package nicfeatures

// PerInterface contains the NIC power model coefficients for a single network
type PerInterface struct {
	UploadPower     float64 `json:"upload_power"`
	DownloadPower   float64 `json:"download_power"`
	UploadMaxRate   float64 `json:"upload_max_rate"`
	DownloadMaxRate float64 `json:"download_max_rate"`
}

// Features maps a network interface name (e.g. "eth0", "wlo1") to its
// per-interface features.
type Features map[string]PerInterface
