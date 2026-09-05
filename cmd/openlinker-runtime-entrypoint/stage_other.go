//go:build !linux

package main

import "errors"

func runAgentStage(string) error {
	return errors.New("official Provider image entrypoint requires Linux")
}
