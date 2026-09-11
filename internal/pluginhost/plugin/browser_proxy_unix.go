//go:build !windows

package plugin

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

func runBrowserProxy(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	getenv func(string) string,
) error {
	if getenv == nil {
		getenv = os.Getenv
	}
	socketPath := filepath.Clean(strings.TrimSpace(getenv("OPENLINKER_BROWSER_TOOL_SOCKET")))
	if !filepath.IsAbs(socketPath) {
		return errors.New("OPENLINKER_BROWSER_TOOL_SOCKET must be an absolute Unix socket path")
	}
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return errors.New("connect to trusted Browser tool broker")
	}
	defer connection.Close()
	type copyResult struct {
		direction string
		err       error
	}
	completed := make(chan copyResult, 2)
	go func() {
		_, copyErr := io.Copy(connection, input)
		if unixConnection, ok := connection.(*net.UnixConn); ok {
			_ = unixConnection.CloseWrite()
		}
		completed <- copyResult{direction: "input", err: copyErr}
	}()
	go func() {
		_, copyErr := io.Copy(output, connection)
		completed <- copyResult{direction: "output", err: copyErr}
	}()
	for received := 0; received < 2; received++ {
		select {
		case <-ctx.Done():
			return nil
		case result := <-completed:
			if result.err != nil {
				return errors.New("Browser tool proxy " + result.direction + " failed")
			}
		}
	}
	return nil
}
