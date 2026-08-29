package core

import (
	"fmt"
	"sort"
	"sync"
)

// OutputRegistry holds the registered output implementations.
type OutputRegistry struct {
	mu       sync.RWMutex
	OutputCreators map[string]OutputCreator
}

// Global output registry where all outputs are/to be registered.
var globalOutputRegistry = InitOutputRegistry()

// Initialize a new output registry.
func InitOutputRegistry() *OutputRegistry {
	return &OutputRegistry{
		OutputCreators: make(map[string]OutputCreator),
	}
}

// GetOutputRegistry returns the global output registry instance.
func GetOutputRegistry() *OutputRegistry {
	return globalOutputRegistry
}

// RegisterOutput registers an output in the global registry.
func RegisterOutput(name string, f OutputCreator) {
	globalOutputRegistry.Register(name, f)
}

// Register associates an output name with its creator.
func (r *OutputRegistry) Register(name string, f OutputCreator) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.OutputCreators[name]; exists {
		panic(fmt.Sprintf("output %q already registered", name))
	}
	r.OutputCreators[name] = f
}

// Names returns the registered output names in sorted order.
func (r *OutputRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.OutputCreators))
	for name := range r.OutputCreators {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// InstantiateEnabledOutputs creates an instance for each enabled output.
func (r *OutputRegistry) InstantiateEnabledOutputs(cfg Config) ([]Output, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var outputs []Output
	for name, enabled := range cfg.Outputs {
		if !enabled {
			continue
		}
		creator, ok := r.OutputCreators[name]
		if !ok {
			return nil, fmt.Errorf("no output implementation registered for %q", name)
		}
		out, err := creator(cfg)
		if err != nil {
			return outputs, fmt.Errorf("instantiating output %q: %w", name, err)
		}
		outputs = append(outputs, out)
	}
	return outputs, nil
}
