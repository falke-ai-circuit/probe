package agent

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// Sensor is a read-only, stateless data source on the agent. Sensors return
// a JSON-marshalable payload. They are called on-demand by the server
// (via TypeSensorRead) and do not maintain state between calls.
//
// Sensors must be OS-independent: use only Go stdlib (runtime, os, net, time).
// No build tags. No /proc, ETW, or platform-specific imports. The same
// sensor definition must compile and run on Windows, Linux, macOS, and
// Android.
type Sensor interface {
	// Name is the canonical sensor key (e.g. "memory_stats").
	Name() string
	// Category groups sensors in the UI: "process" | "filesystem" | "network" | "time" | "agent".
	Category() string
	// Description is shown in the sensor list UI.
	Description() string
	// Read returns the sensor's current value as a JSON-marshalable struct.
	// The return type is `any` so each sensor can define its own payload shape.
	Read(args map[string]string) (any, error)
}

// sensorRegistry holds all registered sensors. It is a fixed set defined
// at package init time via Register — there is no runtime add/remove. The
// server queries the registry via ListSensors and calls ReadSensor by name.
type sensorRegistry struct {
	mu      sync.RWMutex
	sensors map[string]Sensor
}

func newSensorRegistry() *sensorRegistry {
	return &sensorRegistry{sensors: make(map[string]Sensor)}
}

// Register adds a sensor to the global registry. Called from package init
// in sensors_*.go files. Duplicate names overwrite (last write wins) which
// makes tests and overrides safe.
func (r *sensorRegistry) Register(s Sensor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sensors[s.Name()] = s
}

// Get returns a sensor by name.
func (r *sensorRegistry) Get(name string) (Sensor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sensors[name]
	return s, ok
}

// List returns all registered sensors as a sorted slice of metadata.
func (r *sensorRegistry) List() []protocol.SensorInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]protocol.SensorInfo, 0, len(r.sensors))
	for _, s := range r.sensors {
		out = append(out, protocol.SensorInfo{
			Name:        s.Name(),
			Category:    s.Category(),
			Description: s.Description(),
		})
	}
	// Sort by category then name for stable UI ordering.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			if out[j-1].Category < out[j].Category {
				break
			}
			if out[j-1].Category == out[j].Category && out[j-1].Name <= out[j].Name {
				break
			}
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// ReadSensor reads a single sensor by name and returns the JSON payload.
// Errors include unknown sensor name and sensor read failure.
func (r *sensorRegistry) ReadSensor(name string, args map[string]string) (json.RawMessage, error) {
	s, ok := r.Get(name)
	if !ok {
		return nil, fmt.Errorf("unknown sensor %q", name)
	}
	val, err := s.Read(args)
	if err != nil {
		return nil, err
	}
	return json.Marshal(val)
}

// agentSensors is the package-global registry. All sensors register here
// via init(). The Agent struct holds a reference to this same registry.
var agentSensors = newSensorRegistry()

// handleSensorList responds to TypeSensorList with the catalog of sensors.
func (a *Agent) handleSensorList(env protocol.Envelope) protocol.Envelope {
	list := protocol.SensorListResult{Sensors: agentSensors.List()}
	return protocol.NewResult(env.ID, protocol.TypeSensorList, list)
}

// handleSensorRead responds to TypeSensorRead with the sensor's payload.
// The sensor name comes from the request params; args are passed through
// to the sensor's Read method.
func (a *Agent) handleSensorRead(env protocol.Envelope) protocol.Envelope {
	var params protocol.SensorReadParams
	if err := json.Unmarshal(env.Params, &params); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, "invalid sensor params: "+err.Error())
	}
	start := time.Now()
	payload, err := agentSensors.ReadSensor(params.Name, params.Args)
	if err != nil {
		return protocol.NewResult(env.ID, protocol.TypeSensorError, protocol.SensorError{
			Name:    params.Name,
			Message: err.Error(),
		})
	}
	return protocol.NewResult(env.ID, protocol.TypeSensorResult, protocol.SensorResult{
		Name:      params.Name,
		Payload:   payload,
		Timestamp: start.UTC(),
		Duration:  time.Since(start).Microseconds(),
	})
}
