package core

//module interface supported by all modules
type Module interface {
	// Name returns the module identifier, e.g. "cpu", "ram".
	Name() string
	// Start prepares the module. It is called once before the sampling loop.
	Start() error
	// Measure reads the current samples for this module. It is invoked by the
	// engine on every sampling tick.
	Measure() ([]Sample, error)
	// Stop releases any resources held by the module.
	Stop() error
}

//ModuleCreator function creates a module instance based on the given configuration
type ModuleCreator func(cfg Config) (Module, error)
