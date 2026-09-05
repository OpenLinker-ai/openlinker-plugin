package browserprotocol

import (
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol/internal/browsercontract"
)

//go:generate go run ./internal/generatecontract

const (
	BrowserViewportWidth  = browsercontract.ViewportWidth
	BrowserViewportHeight = browsercontract.ViewportHeight

	// BrowserStateRetention is the shared retention window for Browser Profile,
	// page-continuation, and conversation Session state.
	BrowserStateRetention time.Duration = browsercontract.StateRetention

	// BrowserSessionStateLimit bounds durable conversation Session files.
	BrowserSessionStateLimit = browsercontract.SessionStateLimit

	// BrowserSessionStateScanLimit bounds work performed by each acquire.
	BrowserSessionStateScanLimit = browsercontract.SessionStateScanLimit
)
