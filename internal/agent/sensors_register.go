package agent

// init() registers all built-in sensors with the package-global registry.
// Add new sensors by appending to the slice.
func init() {
	for _, s := range []Sensor{
		// Group 1: process
		processDetailSensor{},
		runtimeMetricsSensor{},

		// Group 2: runtime + filesystem
		memoryStatsSensor{},
		diskUsageSensor{},
		fileStatSensor{},
		fileReadSensor{},
		fileWriteSensor{},
		envVarsSensor{},

		// Group 3: network
		networkInterfacesSensor{},
		dnsResolveSensor{},
		dnsResolveMXSensor{},
		dnsResolveTXTSensor{},
		networkDialSensor{},

		// Group 4: time
		systemTimeSensor{},
		uptimeSensor{},
		ntpDriftSensor{},

		// Group 5: agent-internal
		agentMetricsSensor{},
		auditChainSensor{},

	} {
		agentSensors.Register(s)
	}
}
