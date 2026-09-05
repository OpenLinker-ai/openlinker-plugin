package browsercontract

import "time"

const (
	ViewportWidth  = 1280
	ViewportHeight = 720

	StateRetention        = 30 * 24 * time.Hour
	SessionStateLimit     = 256
	SessionStateScanLimit = 512
)
