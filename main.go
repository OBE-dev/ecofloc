package main

import (
	"context"
	"ecofloc/core"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	//each module must be imported to be registered inside its init() function
	_ "ecofloc/modules/cpu"

	//each output must be imported to be registered inside its init() function
	_ "ecofloc/outputs/csvfile"
	_ "ecofloc/outputs/mqtt"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ecofloc:", err)
		os.Exit(1)
	}
}

func run() error {
	// Modules, Methods and Outputs are auto-registred at init time before the main function is called
	// We can then get the list of registered modules, methods and outputs
	moduleRegistry := core.GetModuleRegistry()
	methodRegistry := core.GetMethodRegistry()
	outputRegistry := core.GetOutputRegistry()


	// Get the names of the registered modules
	registeredModules := moduleRegistry.Names()

	// Initialize flag set for the ecofloc command
	fs := flag.NewFlagSet("ecofloc", flag.ContinueOnError)
	// Add a custom usage function
	fs.Usage = EcoflocUsage(fs, registeredModules, methodRegistry)

	// Register a flag for each registered module and for the other parameters and apply a default value
	for _, module := range registeredModules {
		fs.Bool(module, false, fmt.Sprintf("enable the %s module", module))
	}
	interval := fs.Int("t", 0, "total measurement duration in seconds (0 = unlimited)")
	samplingTime := fs.Int("i", 0, "sampling period in milliseconds")
	pid := fs.Int("p", 0, "restrict measurement to this process PID (0 = system-wide)")
	appName := fs.String("n", "", "restrict measurement to a process selected by name")
	outputs := fs.String("o", "", "output modules (comma-separated): csv,mqtt...")
	methods := fs.String("m", "", "measurement method per module (comma-separated): cpu:rapl,ram:standard...")
	configPath := fs.String("c", "", "path to a system.json configuration file")

	// Parse the ecofloc command line arguments (jump over the command name)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err // if the parsed arguments do not match the defined flags, return an error
	}

	// Start with default configuration
	config := core.DefaultConfig

	// If a configuration file is provided, load it as default values
	if *configPath != "" {
		if err := config.LoadConfigFile(*configPath); err != nil {
			return err
		}
	}

	// Track which flags were explicitly set by the user
	// the flags set by the user should override the configuration file values
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	// Modules
	for _, module := range registeredModules {
		if set[module] {
			config.Modules[module] = true
		}
	}
	// Outputs
	// Get the names of the registered outputs
	registeredOutputs := outputRegistry.Names()
	if set["o"] {
		enabledOutputs := strings.Split(*outputs, ",") // outputs is a comma-separated list of enabled outputs
		// first disable all registered outputs (override configuration file)
		for _, output := range registeredOutputs {
			config.Outputs[output] = false
		}
		// then enable the specified outputs
		for _, enabledOutput := range enabledOutputs {
			if !slices.Contains(registeredOutputs, enabledOutput) {
				return fmt.Errorf("unsupported output: %s", enabledOutput)
			}
			config.Outputs[enabledOutput] = true
		}
	}
	// Measurement methods
	if set["m"] {
		// Parse the passed methods arguments into a map[module]method
		methodMap, err := core.ParseMethodPairs(*methods)
		if err != nil {
			return err
		}
		for module, method := range methodMap {
			// Get the names of the registred methods for this module
			registeredMethods := methodRegistry.Names(module)
			// Check if the selected method is supported for this module
			if !slices.Contains(registeredMethods, method) {
				return fmt.Errorf("unsupported method: %s for module: %s", method, module)
			}
			config.MeasurementMethods[module] = method
		}
	}
	// interval
	if set["t"] {
		config.Interval = *interval
	}
	// sampling time
	if set["i"] {
		config.SamplingTime = *samplingTime
	}
	// check for conflicts between app name and pid
	if set["n"] && set["p"] {
		return fmt.Errorf("cannot specify both app name and pid")
	}
	// app name
	if set["n"] {
		config.AppName = *appName
	}
	// pid
	if set["p"] {
		config.PID = *pid
		config.AppName = "" // if pid is specified, clear appname(from the configuration file)
	}

	// validate the final configuration
	if err := config.Validate(); err != nil {
		// print the command line usage and return an error
		fs.Usage()
		return err
	}

	// Resolve the monitoring target (if appname is provided, find the PID, otherwise check the provided PID
	// if neither is provided, run in system-wide mode
	if err := config.ResolveTarget(); err != nil {
		return err
	}

	// Instantiate the enabled modules
	activeModules, err := moduleRegistry.InstantiateEnabledModules(config)
	if err != nil {
		return err
	}
	if len(activeModules) == 0 {
		// print an error and exit
		return fmt.Errorf("no modules enabled")
	}

	// Instantiate the outputs selected in the configuration.
	activeOutputs, err := outputRegistry.InstantiateEnabledOutputs(config)
	if err != nil {
		return err
	}
	if len(activeOutputs) == 0 {
		// print a warning and continue, the final measurement will be shown in the console
		fmt.Fprintln(os.Stderr, "warning: no output enabled")
	}

	// Create an aggregator with the instantiated outputs
	aggregator := core.NewAggregator(activeOutputs...)
	defer aggregator.Close()

	// Create a context that will be cancelled when the program is interrupted (Ctrl-C / SIGTERM)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Instantiate the engine with the configuration, active modules, and aggregator
	engine := core.NewEngine(config, activeModules, aggregator)

	// The engine runs in a loop until the context is cancelled or the measurement duration elapses
	return engine.Run(ctx)
}

// EcoflocUsage adds a custom usage text over the default flag usage
func EcoflocUsage(fs *flag.FlagSet, modules []string, methodRegistry *core.MethodRegistry) func() {
	return func() {
		moduleFlags := make([]string, len(modules))
		for i, m := range modules {
			moduleFlags[i] = "--" + m
		}
		fmt.Fprintf(os.Stderr, `ecofloc — Energy Measuring System Tool

Usage:
  ecofloc [%s] [-t s] [-i ms] [-p pid | -n name] [-o outputs] [-m methods]
  ecofloc -c /path/to/system.json [flag overrides...]

Flags:
`, strings.Join(moduleFlags, " "))
		fs.PrintDefaults() //default flag usage

		fmt.Fprintln(os.Stderr, "\nMeasurement methods per module:")
		for _, module := range modules {
			methodNames := methodRegistry.Names(module)
			if len(methodNames) == 0 {
				fmt.Fprintf(os.Stderr, "  %s: (no measurement method available)\n", module)
				continue
			}
			fmt.Fprintf(os.Stderr, "  %s: %s\n", module, strings.Join(methodNames, ", "))
		}
	}
}
