package core


type Output interface {
	// Name returns the output identifier, e.g. "csv", "mqtt"...
	Name() string
	// Write out the samples through the output interface
	Write(samples []Sample) error
	// Close and releases any resources held by the output module.
	Close() error
}