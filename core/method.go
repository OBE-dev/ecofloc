package core

// Method represents the measurement method used by each module
type Method interface {
    Start() error
    Measure() ([]Sample, error)
    Stop() error
}
 
// MethodCreator creates a measurement method instance based on the given configuration
type MethodCreator func(cfg Config) (Method, error)