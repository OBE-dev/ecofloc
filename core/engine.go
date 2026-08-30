package core

import (
	"context"
	"log"
	"time"
)

// Engine drives the active modules on the shared sampling interval and pushes
// their samples to the aggregator. It runs until the context is cancelled or
// the configured measurement duration elapses (when one is set).
type Engine struct {
	cfg     Config
	modules []Module
	agg     *Aggregator
}

// NewEngine instantiates an Engine for the given modules and aggregator.
func NewEngine(cfg Config, modules []Module, agg *Aggregator) *Engine {
	return &Engine{cfg: cfg, modules: modules, agg: agg}
}

// Run starts every module, then samples them on each tick of the shared
// SamplingCadence. If a measurement duration is configured, the loop stops
// once it is reached; otherwise it runs until the context is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	// Start all active modules
    var started []Module
    for _, m := range e.modules {
        if err := m.Start(); err != nil {
            return err
        }
        started = append(started, m)
    }
	
	// Define a cleanup function to be executed when Run returns
    defer func() {
		// Stop all started modules
        for _, m := range started {
            if err := m.Stop(); err != nil {
                log.Printf("engine: stopping module %q: %v", m.Name(), err)
            }
        }
    }()

	// Create a ticker for the sampling cadence
	samplingTicker := time.NewTicker(e.cfg.SamplingCadence())
	defer samplingTicker.Stop()

	// deadline is a receive only channel that will be triggered when the measurement duration elapses
	var deadline <-chan time.Time
	if d := e.cfg.MeasurementDuration(); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		deadline = t.C
	}

	log.Printf("engine: sampling every %s (duration: %s)",
		e.cfg.SamplingCadence(), durationLabel(e.cfg.MeasurementDuration()))

	// Engine loop
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-deadline:
			return nil
		case <-samplingTicker.C:
			e.sample()
		}
	}
}

// sample the measurement routine for each active module and forwards the results to the aggregator.
func (e *Engine) sample() {
	for _, m := range e.modules {
		samples, err := m.Measure()
		if err != nil {
			log.Printf("engine: module %q measure failed: %v", m.Name(), err)
			continue
		}
		e.agg.Process(samples)
	}
}

func durationLabel(d time.Duration) string {
	if d <= 0 {
		return "unlimited"
	}
	return d.String()
}
