package agent

import "github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/appfiles"

func decodeStrictJSON(raw []byte, target any) error {
	return appfiles.DecodeStrictJSON(raw, target)
}

func writePrivateJSON(path string, value any) error {
	return appfiles.WritePrivateJSON(path, value)
}

func writePrivateFile(path string, raw []byte) error {
	return appfiles.WritePrivateFile(path, raw)
}

func readPrivateSecret(path string) (string, error) { return appfiles.ReadPrivateSecret(path) }
