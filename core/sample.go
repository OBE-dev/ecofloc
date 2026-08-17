package core

import "time"

// Sample is the single measurement unit produced by every activated module on one sampling tick.
// PID is 0 for system-wide measurements.
// Metrics is an open map so each module can report its own parameters.
type Sample struct {
	Module    string             `json:"module"`
	PID       int                `json:"pid,omitempty"`
	Timestamp time.Time          `json:"timestamp"`
	Metrics   map[string]float64 `json:"metrics"`
}
