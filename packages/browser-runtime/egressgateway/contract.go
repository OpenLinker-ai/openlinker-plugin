package egressgateway

import "github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/egressgateway/internal/egresscontract"

//go:generate go run ./internal/generatecontract

const DecisionHeader = egresscontract.DecisionHeader
const HealthPath = egresscontract.HealthPath
const HealthDecision = egresscontract.HealthDecision
