//go:build !linux

package main

const (
	officialBrowserControlRoot = "/browser-control"
	officialBrowserBrokerRoot  = "/browser-tool"
)

func prepareBrowserMounts() error { return nil }
