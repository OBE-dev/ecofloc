package main

import (
	"context"
	"ecofloc/core"
	"ecofloc/outputs/csvfile"
	"ecofloc/outputs/mqtt"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	//each module must be imported to be registered inside its init() function
	_ "ecofloc/modules/cpu"
)

//list of supported modules
var SupportedModules = []string{"cpu", "ram", "disk", "nic", "gpu", "rapl"}
// list of supported outputs
var SupportedOutputs = []string{"csv", "mqtt"}


func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ecofloc:", err)
		os.Exit(1)
	}
}

func run() error {

	// Initialize flag set for the ecofloc command
	fs := flag.NewFlagSet("ecofloc", flag.ContinueOnError)
	// Add a custom usage function
	fs.Usage = EcoflocUsage(fs)

	// Register a flag for each supported module and for the other parameters
	for _, module := range SupportedModules {
		fs.Bool(module, false, fmt.Sprintf("enable the %s module", module))
	}
	interval     := fs.Int("t", 0, "total measurement duration in seconds (0 = unlimited)")
	samplingTime := fs.Int("i", 0, "sampling period in milliseconds")
	pid          := fs.Int("p", 0, "restrict measurement to this process PID (0 = system-wide)")
	appName      := fs.String("n", "", "restrict measurement to a process selected by name")
	outputs      := fs.String("o", "", "output modules (comma-separated): csv,mqtt...")
	configPath   := fs.String("c", "", "path to a system.json configuration file")
	
	// Parse the ecofloc command line arguments (jump over the command name)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err // if the parsed arguments do not match the defined flags, return an error
	}

	// Init empty configuration
	config := core.Config{
		Modules: make(map[string]bool),
		Outputs: make(map[string]bool),
		//other parameters are automatically set to their zero/false/empty values
	}

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
	for _, module := range SupportedModules {
		if set[module] {
			config.Modules[module] = true
		}
	}
	// Outputs
	if set["o"] {
		enabledOutputs := strings.Split(*outputs, ",") // outputs is a comma-separated list of enabled outputs
		// first disable all outputs (override configuration file)
		for _, output := range SupportedOutputs {
			config.Outputs[output] = false
		}
		// then enable the specified outputs (if it's supported)
		for _, enabled := range enabledOutputs {
			if !slices.Contains(SupportedOutputs, enabled) {
				return fmt.Errorf("unsupported output: %s", enabled)
			}
			config.Outputs[enabled] = true
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

	// Get the global modules registry instance where all modules are/to be registered
	registry := core.GetRegistry()

	// Activate all the enabled modules
	missing, err := registry.ActivateEnabledModules(config)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr,
			"warning: no implementation registered for enabled module(s): %s\n",
			strings.Join(missing, ", "))
	}

	// Instantiate the outputs selected in the configuration.
	outputModules, err := InstantiateOutputs(config)
	if err != nil {
		return err
	}

	// Create an aggregator with the instantiated outputs
	aggregator := core.NewAggregator(outputModules...)
	defer aggregator.Close()

	// Create a context that will be cancelled when the program is interrupted (Ctrl-C / SIGTERM)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Instantiate the engine with the configuration, active modules, and aggregator
	engine := core.NewEngine(config, registry.Active(), aggregator)

	// The engine runs in a loop until the context is cancelled or the measurement duration elapses
	return engine.Run(ctx)
}

// EcoflocUsage adds a custom usage text over the default flag usage
func EcoflocUsage(fs *flag.FlagSet) func() {
	return func() {
		fmt.Fprintf(os.Stderr, `ecofloc — per-process energy measurement

Usage:
  ecofloc [--cpu] [--ram] [--disk] [--nic] [--gpu] [--rapl] [-t s] [-i ms] [-p pid | -n name] [-o outputs]
  ecofloc -c /path/to/system.json [flag overrides...]

Flags:
`)
		fs.PrintDefaults() //default flag usage
	}
}


// Instantiate the outputs enabled in the configuration.
func InstantiateOutputs(cfg core.Config) ([]core.Output, error) {
	var outputs []core.Output
	if cfg.Outputs["csv"] {
		csv, err := csvfile.New()
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, csv)
	}
	if cfg.Outputs["mqtt"] {
		mqtt, err := mqtt.New()
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, mqtt)
	}	
	if len(outputs) == 0 {
		fmt.Fprintln(os.Stderr, "warning: no output enabled; samples will be discarded")
	}
	return outputs, nil
}