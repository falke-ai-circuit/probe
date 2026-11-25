package main

// P21-09 — the relay's upstream candidate list parses in both config forms.
//
// The legacy config carries `"upstream": "ws://host:7700/ws"`. The new one
// carries an ordered array under the same key. An operator upgrading the binary
// must not have to rewrite their file, so both must land in one list, in order.

import (
	"reflect"
	"testing"
)

func TestP2109_ConfigUpstreamStringIsOneCandidate(t *testing.T) {
	cfg := []byte(`{
		"relay": {
			"listen": ":7701",
			"upstream": "ws://legacy.example:7700/ws",
			"token": "relay-token"
		}
	}`)
	uc, err := ParseConfig(cfg)
	if err != nil {
		t.Fatalf("parse legacy string form: %v", err)
	}
	if uc.Relay == nil {
		t.Fatal("relay section dropped")
	}
	want := RelayUpstreams{"ws://legacy.example:7700/ws"}
	if !reflect.DeepEqual(uc.Relay.Upstream, want) {
		t.Fatalf("upstream: got %#v, want %#v", uc.Relay.Upstream, want)
	}
	rc := uc.ToRelayConfig()
	if !reflect.DeepEqual(rc.UpstreamURLs, []string(want)) {
		t.Fatalf("relay.Config.UpstreamURLs: got %#v, want %#v", rc.UpstreamURLs, []string(want))
	}
	if rc.UpstreamURL != "ws://legacy.example:7700/ws" {
		t.Fatalf("legacy UpstreamURL not preserved: %q", rc.UpstreamURL)
	}
}

func TestP2109_ConfigUpstreamArrayKeepsOrder(t *testing.T) {
	cfg := []byte(`{
		"relay": {
			"listen": ":7701",
			"upstream": [
				"wss://first.example:443/ws",
				"ws://second.example:80/ws",
				"ws://third.example:7700/ws"
			],
			"token": "relay-token"
		}
	}`)
	uc, err := ParseConfig(cfg)
	if err != nil {
		t.Fatalf("parse array form: %v", err)
	}
	want := []string{
		"wss://first.example:443/ws",
		"ws://second.example:80/ws",
		"ws://third.example:7700/ws",
	}
	if got := []string(uc.Relay.Upstream); !reflect.DeepEqual(got, want) {
		t.Fatalf("upstream order: got %#v, want %#v", got, want)
	}
	rc := uc.ToRelayConfig()
	if !reflect.DeepEqual(rc.UpstreamURLs, want) {
		t.Fatalf("relay.Config.UpstreamURLs: got %#v, want %#v", rc.UpstreamURLs, want)
	}
	// The legacy view names the top of the list, not a sorted or arbitrary one.
	if rc.UpstreamURL != want[0] {
		t.Fatalf("legacy UpstreamURL: got %q, want the first candidate %q", rc.UpstreamURL, want[0])
	}
}

func TestP2109_ConfigUpstreamRejectsNonString(t *testing.T) {
	cfg := []byte(`{"relay":{"upstream":123,"token":"t"}}`)
	if _, err := ParseConfig(cfg); err == nil {
		t.Fatal("numeric upstream accepted; the parse must name the shape it wants")
	}
}
