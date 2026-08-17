package core

import (
	"log"
	"sync"
)

// Aggregator collects samples produced by the enabled modules and forward them out to
// every enabled output.
type Aggregator struct {
	mu      sync.Mutex
	outputs []Output
}

// NewAggregator instantiates an Aggregator wired to the given outputs.
func NewAggregator(outputs ...Output) *Aggregator {
	return &Aggregator{outputs: outputs}
}

// Process forwards a batch of samples to all outputs. A failing output is
// logged and skipped so it cannot block the others.
func (a *Aggregator) Process(samples []Sample) {
	if len(samples) == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, o := range a.outputs {
		if err := o.Write(samples); err != nil {
			log.Printf("aggregator: output %q write failed: %v", o.Name(), err)
		}
	}
}

// Close flushes and releases every output.
func (a *Aggregator) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	var firstErr error
	for _, o := range a.outputs {
		if err := o.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
