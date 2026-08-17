package core

import (
	"fmt"
	"sync"
)


type Registry struct {
	mu        sync.RWMutex //mutex for thread safety
	ModuleCreators map[string]ModuleCreator // list of module creators
	ActiveModules    map[string]Module // list of active modules
}

// Global registry instance where all modules are/to be registered
var globalRegistry = InitRegistry()

// Initialize a new registry
func InitRegistry() *Registry {
	return &Registry{
		ModuleCreators: make(map[string]ModuleCreator),
		ActiveModules:  make(map[string]Module),
	}
}

// GetRegistry returns the global registry instance
func GetRegistry() *Registry {
	return globalRegistry
}

// Register a module in the globalRegistry
func Register(name string, f ModuleCreator) {
	globalRegistry.Register(name, f)
}

// Register a module and associate it with its creator
func (r *Registry) Register(name string, f ModuleCreator) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.ModuleCreators[name]; exists {
		panic(fmt.Sprintf("module %q already registered", name))
	}
	r.ModuleCreators[name] = f
}

// ActivateEnabledModules calls the creator function for each enabled module
func (r *Registry) ActivateEnabledModules(cfg Config) (missing []string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Filter enabled modules
	enabledModules := make([]string, 0)
	for moduleName, isEnabled := range cfg.Modules {
		if isEnabled {
			enabledModules = append(enabledModules, moduleName)
		}
	}
	// Activate each enabled module
	for _, name := range enabledModules {
		moduleCreator, ok := r.ModuleCreators[name]
		if !ok {
			missing = append(missing, name) // an enabled module has not been registered
			continue
		}
		module, ferr := moduleCreator(cfg)
		if ferr != nil {
			return missing, fmt.Errorf("activating module %q: %w", name, ferr)
		}
		r.ActiveModules[name] = module
	}
	return missing, nil
}

// Active returns the list of active modules
func (r *Registry) Active() []Module {
	r.mu.RLock()
	defer r.mu.RUnlock()
	
	var modules []Module
	for _, module := range r.ActiveModules {
		modules = append(modules, module)
	}
	return modules
}