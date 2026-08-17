# EcoFloc V2 — Architecture

The EcoFloc V2 design cleanly separates **what to measure** (modules), **the measurement
loop** (core engine), and **where to send results** (outputs). They are coupled
only through interfaces and a generic `Sample` type, which makes both sides
independently extensible.

---

## Layout

The codebase is organized into three clear layers:

| Path        | Responsibility                                                                    |
|-------------|-----------------------------------------------------------------------------------|
| `main.go`   | CLI parsing, resolve & validate configuration, wire and start pipeline            |
| `core/`     | The framework (interfaces + engine + registry) — **hardware agnostic**            |
| `modules/`  | Measurement plugins: `cpu`, `ram`, `disk`, `nic`, `gpu`, `rapl`, ... (extendable) |
| `outputs/`  | Output plugins: `csvfile`, `mqtt`, ... (extendable)                               |

The `core` framework defines the contracts but knows **nothing** about any
specific module or output. Concrete plugins depend on `core`, never the reverse.

---

## Core contracts

### `Module` interface — the plugin contract for a data **source**

Every data source must implement:

- `Name()` — returns the module identifier (`"cpu"`, `"ram"`, ...)
- `Start()` — prepare the module (e.g. load/attach eBPF programs)
- `Measure()` — read the current samples for this module (called every tick)
- `Stop()` — release any resources held by the module

> Note: each module package also **self-registers** in its `init()` function by
calling `core.Register(name, creator)`. `init()`. This is a Go package function that runs automatically when the package is imported.

### `Output` interface — the plugin contract for an output **destination**

Every output destination must implement:

- `Name()` — returns the output identifier (`"csv"`, `"mqtt"`, ...)
- `Write(samples)` — write the samples to the destination
- `Close()` — close and release the output resources

### `Sample` — the universal exchange unit

```go
type Sample struct {
    Module    string
    PID       int                // 0 = system-wide
    Timestamp time.Time
    Metrics   map[string]float64 // open map: each module reports its own metrics
}
```

Because `Metrics` is an **open map**, a module can report any metrics it likes
without changing the core or the outputs.

### `Registry`, `Engine`, `Aggregator`

- **Registry** — a global lookup table with two maps: `ModuleCreators` (factories
  for every compiled-in module) and `ActiveModules` (instances actually enabled
  for this run).
- **Engine** — the driver loop that ticks on the shared sampling cadence, calls
  `Measure()` on each active module, and forwards results to the aggregator.
- **Aggregator** — collects samples from the modules and fans them out to every
  enabled output, isolating a failing output so it can't block the others.

---

## Pipeline flow

```
                        ┌────────────────────────────────────────────────┐
                        │                    main.go                     │
                        │  parse flags · load system.json · validate     │
                        │  resolve target (pid | name | system-wide)     │
                        │  activate modules · build outputs · run engine │
                        └────────────────────────────────────────────────┘
                                            │
                                            │  builds & starts
                                            ▼
                                    ┌───────────────┐
                                    every sampling tick (-i ms)
                                    │  (driver loop)│
                                    └──────┬────────┘
                                           │  Measure() → measurement tool (exp: eBPF)
                                           ▼   
                                    ┌────────────────┐
                                    │   Aggregator   │   fan-out 
                                    └───────┬────────┘
                                            │     Write([]Sample)
                        ┌───────────────────┼────────────┐
                        ▼                   ▼
                 ┌─────────────┐     ┌─────────────┐
                 │ CSV output  │     │ MQTT output │   ... (extendable)
                 └─────────────┘     └─────────────┘

```

## Why it's easy to extend

### Add a new measurement module (e.g. `newModule`)

1. Create `modules/newModule/newModule.go` with a type implementing the four
   `Module` methods (`Name`, `Start`, `Measure`, `Stop`).
2. Add `func init() { core.Register("newModule", NewModuleCreator) }` to register it.
3. Add a blank import `_ "ecofloc/modules/newModule"` in `main.go` and add
   `"newModule"` to `SupportedModules`.

No changes are needed to the engine, registry, aggregator, or sample type. The
module can emit any metrics it likes through the open `Metrics` map.

### Add a new output (e.g. `newOutput`)

1. Implement the three `Output` methods (`Name`, `Write`, `Close`).
2. Add one branch in `InstantiateOutputs` in `main.go`.

The aggregator picks it up automatically and starts forwarding samples to it.

---

## How the program runs

1. **Parse flags** — one bool per module (`--cpu`, `--ram`, ...), plus `-t`
   (duration s), `-i` (sampling period ms), `-p` (PID), `-n` (app name),
   `-o` (outputs), `-c` (config file).
2. **Build config** — optionally load `system.json` as defaults, then let
   explicitly-set CLI flags override file values.
3. **Validate** — validate the configuration.
4. **Resolve target** — pid or system-wide measurement.
5. **Activate modules** — the registry calls each enabled module's factory.
6. **Build outputs & aggregator** — instantiate selected outputs and wire them
   into the aggregator.
7. **Run the engine** — `Start()` all modules, then on each tick call
   `Measure()` and push samples through the aggregator to every output, until
   the duration elapses or the user interrupts. Finally `Close()` all outputs.
