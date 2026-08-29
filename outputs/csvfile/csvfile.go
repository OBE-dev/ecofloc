// Package csvfile implements a core.Output that appends samples to a CSV file.
package csvfile

import (
	"ecofloc/core"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
)

type CSVOutput struct {
	mu     sync.Mutex
	f      *os.File
	w      *csv.Writer
	header bool
}

// the csv output comfiguration loaded from csvfile.json.
type Config struct {
	// Path of the csv file to be created
	Path string `json:"path"`
	// Append opens the file in append mode instead of truncating it; the
	// header is only written when the file is newly created.
	Append bool `json:"append"`
}

var defaultConfig = Config{
	Path:   "ecofloc_metrics.csv",
	Append: false,
}

// init function will be called when the csvfile package is imported, before the main function
func init() {
	core.RegisterOutput("csv", CSVCreator)
}

// CSVCreator creates a new CSV output instance.
func CSVCreator(_ core.Config) (core.Output, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}

	var (
		f          *os.File
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

// configPath returns the path of the csvfile.json which should be placed in the same directory as the executable
func configPath() (string, error) {
	// Get the directory of the current executable
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("csv: locating executable: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), "csvfile.json"), nil
}

// LoadConfig parses csvfile.json
// If the file cannot be read or parsed, a warning is printed and the default
// configuration is returned.
func LoadConfig() (Config, error) {
	c := defaultConfig
	path, err := configPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: csv: locating config file:", err, "- using default configuration")
		return defaultConfig, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: csv: reading config file:", err, "- using default configuration")
		return defaultConfig, nil
	}
	if err := json.Unmarshal(data, &c); err != nil {
		fmt.Fprintln(os.Stderr, "warning: csv: parsing config file:", err, "- using default configuration")
		return defaultConfig, nil
	}
	if c.Path == "" {
		c.Path = defaultConfig.Path
	}
	if !c.Append {
		c.Append = defaultConfig.Append
	}
	return c, nil
}

// returns the name of the output.
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
