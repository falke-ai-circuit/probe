//go:build !linux && !windows

package main

// managerView on platforms with no supported service manager in this repo:
// say so rather than reporting a state nothing backs.
func managerView(string) string { return "service-manager:none" }
