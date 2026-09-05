package agentexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
)

const maxProviderOutputBytes = 4 << 20

var errProviderOutputTooLarge = errors.New("provider output exceeded limit")

type limitedOutputBuffer struct {
	buf      bytes.Buffer
	cancel   context.CancelFunc
	exceeded bool
}

func newLimitedOutputBuffer(cancel context.CancelFunc) *limitedOutputBuffer {
	return &limitedOutputBuffer{cancel: cancel}
}

func (buffer *limitedOutputBuffer) Write(value []byte) (int, error) {
	remaining := maxProviderOutputBytes - buffer.buf.Len()
	if remaining > 0 {
		if len(value) <= remaining {
			_, _ = buffer.buf.Write(value)
		} else {
			_, _ = buffer.buf.Write(value[:remaining])
		}
	}
	if len(value) > remaining {
		buffer.exceeded = true
		if buffer.cancel != nil {
			buffer.cancel()
		}
		return 0, errProviderOutputTooLarge
	}
	return len(value), nil
}

func (buffer *limitedOutputBuffer) String() string { return buffer.buf.String() }

func outputLimitError(label string, buffers ...*limitedOutputBuffer) error {
	for _, buffer := range buffers {
		if buffer != nil && buffer.exceeded {
			return fmt.Errorf("%s output exceeded %d bytes", label, maxProviderOutputBytes)
		}
	}
	return nil
}

func boundedText(value string, maximum int, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	runes := []rune(value)
	if len(runes) > maximum {
		value = string(runes[:maximum])
	}
	return value
}
