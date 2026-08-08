package agent

import (
	"runtime"
)

// process_detail: returns info about the agent's own process.
// OS-independent: uses only os.Getpid etc. via runtime.
type processDetailSensor struct{}

func (processDetailSensor) Name() string        { return "process_detail" }
func (processDetailSensor) Category() string    { return "process" }
func (processDetailSensor) Description() string { return "Info about the agent process itself" }

func (processDetailSensor) Read(args map[string]string) (any, error) {
	hostname, _ := hostnameFn()
	wd, _ := workingDirFn()
	exe, _ := executableFn()
	// Use the helper functions from agent.go (defined as package-level vars
	// in the test file). We redefine minimal info here using only stdlib.
	pid := pidFn()
	uid, gid := uidGidFn()
	return map[string]any{
		"pid":         pid,
		"uid":         uid,
		"gid":         gid,
		"hostname":    hostname,
		"working_dir": wd,
		"executable":  exe,
		"args_count":  len(argsFn()),
		"env_count":   len(envFn()),
	}, nil
}

// runtime_metrics: returns Go runtime stats.
type runtimeMetricsSensor struct{}

func (runtimeMetricsSensor) Name() string        { return "runtime_metrics" }
func (runtimeMetricsSensor) Category() string    { return "process" }
func (runtimeMetricsSensor) Description() string { return "Go runtime metrics: goroutines, CPU count, version" }

func (runtimeMetricsSensor) Read(args map[string]string) (any, error) {
	return map[string]any{
		"go_version":  runtime.Version(),
		"goos":        runtime.GOOS,
		"goarch":      runtime.GOARCH,
		"num_cpu":     runtime.NumCPU(),
		"num_goroutine": runtime.NumGoroutine(),
		"cgo_call_count": runtime.NumCgoCall(),
		"gomaxprocs":  runtime.GOMAXPROCS(0),
	}, nil
}
