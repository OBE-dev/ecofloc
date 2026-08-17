// Package csvfile implements a core.Output that appends samples to a CSV file.
package csvfile

import (
	"ecofloc/core"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
)

//go:embed csvfile.json
var configJSON []byte

var defaultConfig = Config{
	Path:   "ecofloc_metrics.csv",
	Append: false,
}

// the csv output comfiguration loaded from csv.json.
type Config struct {
	// Path of the csv file to be created
	Path string `json:"path"`
	// Append opens the file in append mode instead of truncating it; the
	// header is only written when the file is newly created.
	Append bool `json:"append"`
}

// LoadConfig parses the embedded csv.json.
func LoadConfig() (Config, error) {
	var c Config
	if err := json.Unmarshal(configJSON, &c); err != nil {
		return c, fmt.Errorf("csv: parsing csv.json: %w", err)
	}
	if c.Path == "" {
		c.Path = defaultConfig.Path
	}
	if c.Append == false {
		c.Append = defaultConfig.Append
	}
	return c, nil
}

type CSVOutput struct {
	mu     sync.Mutex
	f      *os.File
	w      *csv.Writer
	header bool
}

// New instantiates a CSV output using the settings from csv.json.
func New() (*CSVOutput, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}

	var (
		f *os.File
		needHeader bool
	)
	if cfg.Append {
		// In Append mode: Add a header only if the file is new or empty
		if info, statErr := os.Stat(cfg.Path); statErr != nil || info.Size() == 0 {
			needHeader = true
		}
		f, err = os.OpenFile(cfg.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	} else {
		// In non-append mode: Always add a header
		needHeader = true
		f, err = os.Create(cfg.Path)
	}
	if err != nil {
		return nil, fmt.Errorf("csv: opening %q: %w", cfg.Path, err)
	}

	o := &CSVOutput{f: f, w: csv.NewWriter(f)}
	if needHeader {
		if err := o.w.Write([]string{"timestamp", "module", "pid", "metric", "value"}); err != nil {
			f.Close()
			return nil, fmt.Errorf("csv: writing header: %w", err)
		}
		o.header = true
		o.w.Flush()
	}
	return o, o.w.Error()
}

// Name implements core.Output.
func (o *CSVOutput) Name() string { return "csv" }

// Write appends the samples to the CSV file.
func (o *CSVOutput) Write(samples []core.Sample) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	for _, s := range samples {
		ts := s.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z07:00")
		keys := make([]string, 0, len(s.Metrics))
		for k := range s.Metrics {
			keys = append(keys, k)
		}
		// Sort the metrics keys to ensure deterministic output
		sort.Strings(keys)

		for _, k := range keys {
			line := []string{
				ts,
				s.Module,
				strconv.Itoa(s.PID), // convert PID to string
				k,
				strconv.FormatFloat(s.Metrics[k], 'f', -1, 64), // convert metric value to string
			}
			if err := o.w.Write(line); err != nil {
				return fmt.Errorf("csv: writing line: %w", err)
			}
		}
	}
	o.w.Flush()
	return o.w.Error()
}

// Close flushes and closes the underlying file.
func (o *CSVOutput) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.w.Flush()
	if err := o.w.Error(); err != nil {
		o.f.Close()
		return err
	}
	return o.f.Close()
}
