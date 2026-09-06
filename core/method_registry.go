package core

import (
	"fmt"
	"sort"
	"sync"
)

type MethodRegistry struct {
    mu       sync.RWMutex
    MethodCreators map[string]map[string]MethodCreator // module -> method -> creator
}
 
//Global method registry where all methods are/to be registered (per module)
var globalMethodRegistry = InitMethodRegistry()

// Initialize a new method registry
func InitMethodRegistry() *MethodRegistry {
	return &MethodRegistry{
		MethodCreators: make(map[string]map[string]MethodCreator),
	}
}

func GetMethodRegistry() *MethodRegistry {
	return globalMethodRegistry
}

// RegisterMethod registers a method creator for a specific module
func RegisterMethod(moduleName, methodName string, f MethodCreator) {
	globalMethodRegistry.Register(moduleName, methodName, f)
}
 
// Register registers a method creator for a specific module
func (r *MethodRegistry) Register(moduleName, methodName string, f MethodCreator) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Initialize the module map if it doesn't exist
	if r.MethodCreators[moduleName] == nil {
		r.MethodCreators[moduleName] = make(map[string]MethodCreator)
	}
	// Check if the method is already registered
	if _, exists := r.MethodCreators[moduleName][methodName]; exists {
		panic(fmt.Sprintf("method %q for module %q already registered", methodName, moduleName))
	}
	
	r.MethodCreators[moduleName][methodName] = f
}
 
// GetMethodCreator returns the method creator for a specific module and method
func GetMethodCreator(moduleName, methodName string) (MethodCreator, error) {
    r := globalMethodRegistry
    r.mu.RLock()
    defer r.mu.RUnlock()
 
    methods, ok := r.MethodCreators[moduleName]
    if !ok {
        return nil, fmt.Errorf("no methods registered for module %q", moduleName)
    }
    creator, ok := methods[methodName]
    if !ok {
        return nil, fmt.Errorf("module %q has no method %q", moduleName, methodName)
    }
    return creator, nil
}

// Names returns the registered method names for a specific module
func (r *MethodRegistry) Names(moduleName string) []string {
    r.mu.RLock()
    defer r.mu.RUnlock()

	names := make([]string, 0, len(r.MethodCreators[moduleName]))
    for name := range r.MethodCreators[moduleName] {
        names = append(names, name)
    }
	sort.Strings(names)
    return names
}