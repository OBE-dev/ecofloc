package diskfeatures

// PerDisk contains the disk power model coefficients for a single block device.
type PerDisk struct {
	ReadPower    float64 `json:"read_power"`
	WritePower   float64 `json:"write_power"`
	ReadMaxRate  float64 `json:"read_max_rate"`
	WriteMaxRate float64 `json:"write_max_rate"`
}

// Features maps a block device name (e.g. "nvme0n1") to its
// per-disk features.
type Features map[string]PerDisk
