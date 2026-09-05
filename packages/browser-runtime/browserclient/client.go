package browserclient

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

const (
	LeaseContractID          = "openlinker.browser.lease.v2"
	DefaultSocketPath        = "/browser-control/openlinker.browser.sock"
	defaultTimeout           = 20 * time.Second
	maxCredentialFileBytes   = 4096
	maxLeaseFileBytes        = 16 << 10
	maxChannelCredentialSize = 512
)

type Config struct {
	SocketPath     string
	CredentialFile string
	LeaseFile      string
	Timeout        time.Duration
	Now            func() time.Time
}

type Lease struct {
	ContractID string                   `json:"contract_id"`
	ExpiresAt  time.Time                `json:"expires_at"`
	Identity   browserprotocol.Identity `json:"identity"`
}

type Client struct {
	socketPath        string
	channelCredential string
	lease             Lease
	timeout           time.Duration
	now               func() time.Time
}

func NewFromEnv(getenv func(string) string) (*Client, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	socketPath := strings.TrimSpace(getenv("OPENLINKER_BROWSER_SOCKET"))
	if socketPath == "" {
		socketPath = DefaultSocketPath
	}
	return New(Config{
		SocketPath:     socketPath,
		CredentialFile: getenv("OPENLINKER_BROWSER_CHANNEL_CREDENTIAL_FILE"),
		LeaseFile:      getenv("OPENLINKER_BROWSER_LEASE_FILE"),
	})
}

// LoadLeaseIdentityFromEnv reads only the owner-protected Browser lease. It is
// used by the native MCP surface to publish the authority-matched tool schema
// before the first Browser action, without opening the Browser channel.
func LoadLeaseIdentityFromEnv(
	getenv func(string) string,
) (browserprotocol.Identity, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	lease, err := loadLease(getenv("OPENLINKER_BROWSER_LEASE_FILE"), time.Now().UTC())
	if err != nil {
		return browserprotocol.Identity{}, err
	}
	return lease.Identity, nil
}

func New(config Config) (*Client, error) {
	socketPath := filepath.Clean(strings.TrimSpace(config.SocketPath))
	if !filepath.IsAbs(socketPath) {
		return nil, errors.New("browser socket path must be absolute")
	}
	credentialRaw, err := readOwnerOnlyFile(
		config.CredentialFile,
		maxCredentialFileBytes,
		"Browser channel credential",
	)
	if err != nil {
		return nil, err
	}
	channelCredential := strings.TrimSuffix(string(credentialRaw), "\n")
	channelCredential = strings.TrimSuffix(channelCredential, "\r")
	if len(channelCredential) < 32 ||
		len(channelCredential) > maxChannelCredentialSize ||
		strings.ContainsAny(channelCredential, "\r\n\t ") {
		return nil, errors.New("Browser channel credential value is invalid")
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	lease, err := loadLease(config.LeaseFile, now().UTC())
	if err != nil {
		return nil, err
	}
	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if timeout < time.Second || timeout > browserprotocol.MaxActionDeadline {
		return nil, errors.New("browser client timeout is invalid")
	}
	return &Client{
		socketPath:        socketPath,
		channelCredential: channelCredential,
		lease:             lease,
		timeout:           timeout,
		now:               now,
	}, nil
}

func loadLease(path string, now time.Time) (Lease, error) {
	leaseRaw, err := readOwnerOnlyFile(
		path,
		maxLeaseFileBytes,
		"Browser lease",
	)
	if err != nil {
		return Lease{}, err
	}
	var lease Lease
	if err := decodeStrictJSON(leaseRaw, &lease); err != nil {
		return Lease{}, errors.New("Browser lease is invalid")
	}
	if failure := lease.Validate(now.UTC()); failure != nil {
		return Lease{}, failure
	}
	return lease, nil
}

func (lease Lease) Validate(now time.Time) *browserprotocol.Failure {
	if lease.ContractID != LeaseContractID {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorProtocolInvalid,
			"browser lease contract is invalid",
			false,
		)
	}
	if failure := lease.Identity.Validate(); failure != nil {
		return failure
	}
	if lease.ExpiresAt.IsZero() || !lease.ExpiresAt.UTC().After(now.UTC()) {
		return browserprotocol.NewFailure(
			browserprotocol.ErrorDeadlineExceeded,
			"browser lease has expired",
			false,
		)
	}
	return nil
}

func (client *Client) Identity() browserprotocol.Identity {
	if client == nil {
		return browserprotocol.Identity{}
	}
	return client.lease.Identity
}

func (client *Client) Execute(
	ctx context.Context,
	action browserprotocol.Action,
) (browserprotocol.Observation, *browserprotocol.Failure) {
	if client == nil {
		return browserprotocol.Observation{}, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser client is not configured",
			false,
		)
	}
	if failure := action.ValidateForPolicy(client.lease.Identity.BrowserInteractionPolicy); failure != nil {
		return browserprotocol.Observation{}, failure
	}
	now := client.now().UTC()
	if failure := client.lease.Validate(now); failure != nil {
		return browserprotocol.Observation{}, failure
	}
	deadline := now.Add(client.timeout)
	if client.lease.ExpiresAt.Before(deadline) {
		deadline = client.lease.ExpiresAt.UTC()
	}
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline.UTC()
	}
	requestID, err := newRequestID()
	if err != nil {
		return browserprotocol.Observation{}, browserprotocol.NewFailure(
			browserprotocol.ErrorInternal,
			"generate browser request identifier",
			true,
		)
	}
	request := browserprotocol.Request{
		ContractID:        browserprotocol.ContractID,
		ChannelCredential: client.channelCredential,
		RequestID:         requestID,
		Deadline:          deadline,
		Identity:          client.lease.Identity,
		Action:            action,
	}
	response, failure := exchange(ctx, client.socketPath, request, deadline)
	if failure != nil {
		return browserprotocol.Observation{}, failure
	}
	if response.ContractID != browserprotocol.ContractID ||
		response.RequestID != requestID {
		return browserprotocol.Observation{}, browserprotocol.NewFailure(
			browserprotocol.ErrorOutputInvalid,
			"browser response identity is invalid",
			false,
		)
	}
	switch response.Status {
	case "ok":
		if response.Error != nil || response.Observation == nil {
			return browserprotocol.Observation{}, browserprotocol.NewFailure(
				browserprotocol.ErrorOutputInvalid,
				"browser success response is invalid",
				false,
			)
		}
		var failure *browserprotocol.Failure
		if action.Kind == browserprotocol.ActionClose {
			failure = response.Observation.ValidateClosed()
		} else {
			failure = response.Observation.ValidateEngine()
		}
		if failure != nil {
			return browserprotocol.Observation{}, failure
		}
		return *response.Observation, nil
	case "error":
		if response.Error == nil || response.Observation != nil {
			return browserprotocol.Observation{}, browserprotocol.NewFailure(
				browserprotocol.ErrorOutputInvalid,
				"browser error response is invalid",
				false,
			)
		}
		if failure := browserprotocol.ValidateFailure(response.Error); failure != nil {
			return browserprotocol.Observation{}, failure
		}
		return browserprotocol.Observation{}, response.Error
	default:
		return browserprotocol.Observation{}, browserprotocol.NewFailure(
			browserprotocol.ErrorOutputInvalid,
			"browser response status is invalid",
			false,
		)
	}
}

func (client *Client) ExecuteViewer(
	ctx context.Context,
	operation browserprotocol.ViewerOperation,
	input *browserprotocol.ViewerInput,
) (*browserprotocol.ViewerFrame, *browserprotocol.Failure) {
	if client == nil {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorRuntimeUnavailable,
			"browser Viewer client is not configured",
			false,
		)
	}
	now := client.now().UTC()
	if failure := client.lease.Validate(now); failure != nil {
		return nil, failure
	}
	deadline := now.Add(client.timeout)
	if client.lease.ExpiresAt.Before(deadline) {
		deadline = client.lease.ExpiresAt.UTC()
	}
	if contextDeadline, ok := ctx.Deadline(); ok &&
		contextDeadline.Before(deadline) {
		deadline = contextDeadline.UTC()
	}
	requestID, err := newRequestID()
	if err != nil {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorInternal,
			"generate browser Viewer request identifier",
			true,
		)
	}
	request := browserprotocol.ViewerRequest{
		ContractID:        browserprotocol.ViewerContractID,
		ChannelCredential: client.channelCredential,
		RequestID:         requestID,
		Deadline:          deadline,
		Identity:          client.lease.Identity,
		Operation:         operation,
		Input:             input,
	}
	if failure := request.Validate(now); failure != nil {
		return nil, failure
	}
	response, failure := exchangeViewer(ctx, client.socketPath, request, deadline)
	if failure != nil {
		return nil, failure
	}
	if response.ContractID != browserprotocol.ViewerContractID ||
		response.RequestID != requestID {
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorOutputInvalid,
			"browser Viewer response identity is invalid",
			false,
		)
	}
	switch response.Status {
	case "ok":
		if response.Error != nil ||
			(operation == browserprotocol.ViewerOperationFrame) !=
				(response.Frame != nil) {
			return nil, browserprotocol.NewFailure(
				browserprotocol.ErrorOutputInvalid,
				"browser Viewer success response is invalid",
				false,
			)
		}
		if response.Frame != nil {
			if validationFailure := response.Frame.Validate(); validationFailure != nil {
				return nil, validationFailure
			}
		}
		return response.Frame, nil
	case "error":
		if response.Error == nil || response.Frame != nil {
			return nil, browserprotocol.NewFailure(
				browserprotocol.ErrorOutputInvalid,
				"browser Viewer error response is invalid",
				false,
			)
		}
		if validationFailure := browserprotocol.ValidateFailure(response.Error); validationFailure != nil {
			return nil, validationFailure
		}
		return nil, response.Error
	default:
		return nil, browserprotocol.NewFailure(
			browserprotocol.ErrorOutputInvalid,
			"browser Viewer response status is invalid",
			false,
		)
	}
}

func readOwnerOnlyFile(rawPath string, limit int64, label string) ([]byte, error) {
	path := filepath.Clean(strings.TrimSpace(rawPath))
	if !filepath.IsAbs(path) {
		return nil, errors.New(label + " path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New(label + " must be a regular non-symlink file")
	}
	if info.Mode().Perm()&0o077 != 0 || !fileOwnedByCurrentUser(info) {
		return nil, errors.New(label + " must be owner-only")
	}
	if info.Size() < 1 || info.Size() > limit {
		return nil, errors.New(label + " length is invalid")
	}
	file, err := os.Open(path) // #nosec G304 -- the operator path is validated above.
	if err != nil {
		return nil, errors.New("open " + label)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("read " + label)
	}
	return raw, nil
}

func decodeStrictJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bufio.NewReaderSize(strings.NewReader(string(raw)), len(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing data")
	}
	return nil
}

func newRequestID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" +
		encoded[16:20] + "-" + encoded[20:32], nil
}
