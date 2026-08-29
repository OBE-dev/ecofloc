package core

import (
	"fmt"
	"sort"
	"sync"
)


type ModuleRegistry struct {
	mu        sync.RWMutex //mutex for thread safety
	ModuleCreators map[string]ModuleCreator // list of module creators
}

// Global module registry where all modules are/to be registered
var globalModuleRegistry = InitModuleRegistry()

// Initialize a new module registry
func InitModuleRegistry() *ModuleRegistry {
	return &ModuleRegistry{
		ModuleCreators: make(map[string]ModuleCreator),
	}
}

// GetRegistry returns the global registry instance
func GetModuleRegistry() *ModuleRegistry {
	return globalModuleRegistry
}

// Register a module in the globalRegistry
func RegisterModule(name string, f ModuleCreator) {
	globalModuleRegistry.Register(name, f)
}

// Register a module and associate it with its creator
func (r *ModuleRegistry) Register(name string, f ModuleCreator) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.ModuleCreators[name]; exists {
		panic(fmt.Sprintf("module %q already registered", name))
	}
	r.ModuleCreators[name] = f
}

// Names returns the registered module names in sorted order.
func (r *ModuleRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.ModuleCreators))
	for name := range r.ModuleCreators {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// InstantiateEnabledModules creates an instance for each enabled module
func (r *ModuleRegistry) InstantiateEnabledModules(cfg Config) ([]Module, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var modules []Module
	for name, enabled := range cfg.Modules {
		if !enabled {
			continue
		}
		creator, ok := r.ModuleCreators[name]
		if !ok {
			return nil, fmt.Errorf("no module implementation registered for %q", name)
		}
		module, err := creator(cfg)
		if err != nil {
			return nil, fmt.Errorf("instantiating module %q: %w", name, err)
		}
		modules = append(modules, module)
	}
	return modules, nil
}
