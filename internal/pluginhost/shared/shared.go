package shared

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

const SDKAgent = "openlinker-plugin-host/0.1"

type IO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
}

type GlobalOptions struct {
	APIBase   string
	UserToken string
	Timeout   time.Duration
}

func (io IO) Env(key string) string {
	if io.Getenv == nil {
		return os.Getenv(key)
	}
	return io.Getenv(key)
}

func DefaultGlobalOptions(getenv func(string) string) GlobalOptions {
	if getenv == nil {
		getenv = os.Getenv
	}
	return GlobalOptions{
		APIBase:   FirstNonEmpty(getenv("OPENLINKER_API_BASE"), getenv("OPENLINKER_URL"), "http://localhost:8080"),
		UserToken: strings.TrimSpace(getenv("OPENLINKER_USER_TOKEN")),
		Timeout:   60 * time.Second,
	}
}

func ContextForOptions(opts GlobalOptions) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), opts.Timeout)
}

func UserClient(opts GlobalOptions) (*openlinker.Client, error) {
	httpClient := &http.Client{Timeout: opts.Timeout}
	options := []openlinker.Option{
		openlinker.WithHTTPClient(httpClient),
		openlinker.WithSDKAgent(SDKAgent),
	}
	if strings.TrimSpace(opts.UserToken) != "" {
		options = append(options, openlinker.WithUserToken(opts.UserToken))
	}
	return openlinker.NewClient(opts.APIBase, options...)
}

func WriteJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type StringList []string

func (l *StringList) String() string {
	if l == nil {
		return ""
	}
	return strings.Join(*l, ",")
}

func (l *StringList) Set(value string) error {
	if strings.TrimSpace(value) != "" {
		*l = append(*l, strings.TrimSpace(value))
	}
	return nil
}

func (l *StringList) Type() string {
	return "stringList"
}
