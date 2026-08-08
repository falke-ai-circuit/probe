package agent

import (
	"net"
	"time"
)

// network_interfaces: returns a list of network interfaces with flags,
// MAC, and IPs. Uses only net.Interfaces (stdlib, OS-independent).
type networkInterfacesSensor struct{}

func (networkInterfacesSensor) Name() string { return "network_interfaces" }
func (networkInterfacesSensor) Category() string {
	return "network"
}
func (networkInterfacesSensor) Description() string {
	return "List of network interfaces with flags, MAC, and IPs"
}

func (networkInterfacesSensor) Read(args map[string]string) (any, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(ifaces))
	for _, iface := range ifaces {
		addrs, _ := iface.Addrs()
		ips := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			ips = append(ips, addr.String())
		}
		out = append(out, map[string]any{
			"name":  iface.Name,
			"flags": iface.Flags.String(),
			"mac":   iface.HardwareAddr.String(),
			"ips":   ips,
		})
	}
	return out, nil
}

// dns_resolve: resolves a hostname. Caller provides hostname via args.
type dnsResolveSensor struct{}

func (dnsResolveSensor) Name() string        { return "dns_resolve" }
func (dnsResolveSensor) Category() string    { return "network" }
func (dnsResolveSensor) Description() string { return "Resolve a hostname to IPs (caller-supplied)" }

func (dnsResolveSensor) Read(args map[string]string) (any, error) {
	host := args["hostname"]
	if host == "" {
		return nil, errInvalidArg("hostname required")
	}
	start := time.Now()
	ips, err := net.LookupHost(host)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"hostname":  host,
		"ips":       ips,
		"lookup_ms": time.Since(start).Milliseconds(),
	}, nil
}

// dns_resolve_mx: returns MX records for a hostname.
type dnsResolveMXSensor struct{}

func (dnsResolveMXSensor) Name() string        { return "dns_resolve_mx" }
func (dnsResolveMXSensor) Category() string    { return "network" }
func (dnsResolveMXSensor) Description() string { return "Resolve MX records for a hostname" }

func (dnsResolveMXSensor) Read(args map[string]string) (any, error) {
	host := args["hostname"]
	if host == "" {
		return nil, errInvalidArg("hostname required")
	}
	mxs, err := net.LookupMX(host)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(mxs))
	for _, mx := range mxs {
		out = append(out, map[string]any{
			"host":  mx.Host,
			"pref":  mx.Pref,
		})
	}
	return out, nil
}

// dns_resolve_txt: returns TXT records for a hostname.
type dnsResolveTXTSensor struct{}

func (dnsResolveTXTSensor) Name() string        { return "dns_resolve_txt" }
func (dnsResolveTXTSensor) Category() string    { return "network" }
func (dnsResolveTXTSensor) Description() string { return "Resolve TXT records for a hostname" }

func (dnsResolveTXTSensor) Read(args map[string]string) (any, error) {
	host := args["hostname"]
	if host == "" {
		return nil, errInvalidArg("hostname required")
	}
	txts, err := net.LookupTXT(host)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"hostname": host,
		"records":  txts,
	}, nil
}

// network_dial: dial a host:port and report latency. Caller provides
// host:port via args["target"] and timeout_ms.
type networkDialSensor struct{}

func (networkDialSensor) Name() string        { return "network_dial" }
func (networkDialSensor) Category() string    { return "network" }
func (networkDialSensor) Description() string { return "TCP dial a target and report latency (ms)" }

func (networkDialSensor) Read(args map[string]string) (any, error) {
	target := args["target"]
	if target == "" {
		return nil, errInvalidArg("target required (host:port)")
	}
	timeoutMs := 5000
	if t, ok := args["timeout_ms"]; ok {
		var v int
		if _, err := fmtSscan(t, &v); err == nil {
			timeoutMs = v
		}
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", target, time.Duration(timeoutMs)*time.Millisecond)
	latency := time.Since(start)
	if err != nil {
		return map[string]any{
			"target":     target,
			"success":    false,
			"error":      err.Error(),
			"latency_ms": latency.Milliseconds(),
		}, nil
	}
	conn.Close()
	return map[string]any{
		"target":     target,
		"success":    true,
		"latency_ms": latency.Milliseconds(),
	}, nil
}

func errInvalidArg(msg string) error { return &sensorArgError{msg: msg} }

type sensorArgError struct{ msg string }

func (e *sensorArgError) Error() string { return e.msg }
