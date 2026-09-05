//go:build !linux && !windows

package main

import "errors"

func prepareRuntimeIdentity() error {
	return errors.New("live acceptance requires the official Linux Provider image")
}
