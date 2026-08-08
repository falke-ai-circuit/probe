package agent

import "time"

// system_time: returns the current UTC time as RFC3339 and Unix epoch.
type systemTimeSensor struct{}

func (systemTimeSensor) Name() string        { return "system_time" }
func (systemTimeSensor) Category() string    { return "time" }
func (systemTimeSensor) Description() string { return "Current time as UTC RFC3339 and Unix epoch" }

func (systemTimeSensor) Read(args map[string]string) (any, error) {
	now := time.Now().UTC()
	return map[string]any{
		"utc":          now.Format(time.RFC3339Nano),
		"unix_seconds": now.Unix(),
		"unix_nanos":   now.UnixNano(),
		"tz_local":     time.Now().Format(time.RFC3339),
	}, nil
}

// uptime: returns the agent's process uptime. Computed from process start
// time captured at package init.
type uptimeSensor struct{}

var processStartTime = time.Now()

func (uptimeSensor) Name() string        { return "uptime" }
func (uptimeSensor) Category() string    { return "time" }
func (uptimeSensor) Description() string { return "Agent process uptime in seconds" }

func (uptimeSensor) Read(args map[string]string) (any, error) {
	return map[string]any{
		"uptime_seconds": int64(time.Since(processStartTime).Seconds()),
		"start_time":     processStartTime.UTC().Format(time.RFC3339),
	}, nil
}

// ntp_drift: queries an NTP server and reports the drift vs local clock.
// Uses only stdlib (net.Dial + time).
type ntpDriftSensor struct{}

func (ntpDriftSensor) Name() string        { return "ntp_drift" }
func (ntpDriftSensor) Category() string    { return "time" }
func (ntpDriftSensor) Description() string { return "NTP query and clock drift vs local (ms)" }

func (ntpDriftSensor) Read(args map[string]string) (any, error) {
	server := args["server"]
	if server == "" {
		server = "time.google.com:123"
	}
	drift, err := queryNTP(server)
	if err != nil {
		return map[string]any{
			"server":  server,
			"error":    err.Error(),
			"drift_ms": 0,
		}, nil
	}
	return map[string]any{
		"server":        server,
		"drift_ms":      drift.Milliseconds(),
		"local_utc":     time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}
