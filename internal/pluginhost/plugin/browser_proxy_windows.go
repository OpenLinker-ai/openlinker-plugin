package plugin

import (
	"context"
	"errors"
	"io"
)

func runBrowserProxy(
	context.Context,
	io.Reader,
	io.Writer,
	func(string) string,
) error {
	return errors.New("Browser tool proxy requires a Linux container or Unix host")
}
