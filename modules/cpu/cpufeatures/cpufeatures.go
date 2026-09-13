package cpufeatures

// Features contains CPU parameters.
type Features struct {
	TDP        float64 `json:"cpu_tdp"`
	FreqTDP    float64 `json:"cpu_freq_tdp"`
	VoltageTDP float64 `json:"cpu_voltage_tdp"`
}
