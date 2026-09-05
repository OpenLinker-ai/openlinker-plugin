//go:build !windows

package browserruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserclient"
	"github.com/OpenLinker-ai/openlinker-plugin/packages/browser-runtime/browserprotocol"
)

type opsObserverTestEngine struct {
	busy                 bool
	runNotActiveFailures int
}

func (engine *opsObserverTestEngine) ObserveOps(
	_ context.Context,
	_ string,
	operation browserprotocol.OpsObserverOperation,
) (browserprotocol.OpsObserverObservation, bool, *browserprotocol.OpsObserverError) {
	if engine.runNotActiveFailures > 0 {
		engine.runNotActiveFailures--
		return browserprotocol.OpsObserverObservation{}, false,
			browserprotocol.NewOpsObserverError(
				browserprotocol.OpsObserverRunNotActive,
				"requested Run is not active",
			)
	}
	if engine.busy {
		return browserprotocol.OpsObserverObservation{}, true, nil
	}
	observation := browserprotocol.OpsObserverObservation{
		RunID:                "11111111-1111-4111-8111-111111111111",
		Controller:           browserprotocol.ControllerAgent,
		SessionEpoch:         1,
		ControlEpoch:         3,
		BrowserSessionSHA256: strings.Repeat("a", 64),
		AttachmentSHA256:     strings.Repeat("b", 64),
		SelectedBackend:      BackendOfficialChrome,
		ProfileGeneration:    2,
		PageURL:              "about:blank",
		PageTitle:            "Blank",
	}
	if operation == browserprotocol.OpsObserverFrameOperation {
		observation.Frame = &browserprotocol.ViewerFrame{
			MIMEType: "image/jpeg",
			Data:     []byte{0xff, 0xd8, 0xff, 0xd9},
			Width:    1280,
			Height:   720,
		}
	}
	return observation, false, nil
}

func TestOpsObserverServerEnforcesOneConnectionBoundLease(t *testing.T) {
	t.Parallel()
	root, err := os.MkdirTemp("", "ol-ops-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	credentialFile := filepath.Join(root, "credential")
	credential := strings.Repeat("c", 64)
	if err := os.WriteFile(credentialFile, []byte(credential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(root, "observer.sock")
	observer := &opsObserverTestEngine{}
	server, err := NewOpsObserverServer(OpsObserverServerOptions{
		SocketPath:        socketPath,
		ChannelCredential: credential,
		Observer:          observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	if err := server.open(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- server.Serve(ctx) }()

	first := newOpsObserverTestStream(t, ctx, socketPath, credentialFile)
	response, observerErr := first.Observe(ctx, browserprotocol.OpsObserverFrameOperation)
	if observerErr != nil || response.Status != "ok" ||
		response.Observation.FrameSequence != 1 {
		t.Fatalf("first observation = %#v, %v", response, observerErr)
	}

	second := newOpsObserverTestStream(t, ctx, socketPath, credentialFile)
	_, observerErr = second.Observe(ctx, browserprotocol.OpsObserverStatusOperation)
	if observerErr == nil || observerErr.Code != browserprotocol.OpsObserverAlreadyActive {
		t.Fatalf("second observer error = %v", observerErr)
	}
	_ = second.Close()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	var third *browserclient.OpsObserverStream
	releaseDeadline := time.Now().Add(2 * time.Second)
	for {
		third = newOpsObserverTestStream(t, ctx, socketPath, credentialFile)
		response, observerErr = third.Observe(ctx, browserprotocol.OpsObserverStatusOperation)
		if observerErr == nil {
			break
		}
		_ = third.Close()
		if observerErr.Code != browserprotocol.OpsObserverAlreadyActive ||
			time.Now().After(releaseDeadline) {
			t.Fatalf("replacement observation = %#v, %v", response, observerErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if response.Status != "ok" || response.Observation.Frame != nil {
		t.Fatalf("replacement observation = %#v, %v", response, observerErr)
	}
	_ = third.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOpsObserverServerReturnsBusyWithoutConsumingFrameSequence(t *testing.T) {
	t.Parallel()
	root, err := os.MkdirTemp("", "ol-ops-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	credentialFile := filepath.Join(root, "credential")
	credential := strings.Repeat("d", 64)
	if err := os.WriteFile(credentialFile, []byte(credential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(root, "observer.sock")
	observer := &opsObserverTestEngine{busy: true}
	server, err := NewOpsObserverServer(OpsObserverServerOptions{
		SocketPath: socketPath, ChannelCredential: credential, Observer: observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	if err := server.open(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- server.Serve(ctx) }()
	stream := newOpsObserverTestStream(t, ctx, socketPath, credentialFile)
	response, observerErr := stream.Observe(ctx, browserprotocol.OpsObserverFrameOperation)
	if observerErr != nil || response.Status != "busy" {
		t.Fatalf("busy observation = %#v, %v", response, observerErr)
	}
	observer.busy = false
	response, observerErr = stream.Observe(ctx, browserprotocol.OpsObserverStatusOperation)
	if observerErr != nil || response.Observation.FrameSequence != 1 {
		t.Fatalf("post-busy observation = %#v, %v", response, observerErr)
	}
	_ = stream.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestOpsObserverServerKeepsTheLeaseAcrossARunActionGap(t *testing.T) {
	t.Parallel()
	root, err := os.MkdirTemp("", "ol-ops-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	credentialFile := filepath.Join(root, "credential")
	credential := strings.Repeat("e", 64)
	if err := os.WriteFile(credentialFile, []byte(credential+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(root, "observer.sock")
	observer := &opsObserverTestEngine{runNotActiveFailures: 1}
	server, err := NewOpsObserverServer(OpsObserverServerOptions{
		SocketPath: socketPath, ChannelCredential: credential, Observer: observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	if err := server.open(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- server.Serve(ctx) }()
	stream := newOpsObserverTestStream(t, ctx, socketPath, credentialFile)

	_, observerErr := stream.Observe(ctx, browserprotocol.OpsObserverFrameOperation)
	if observerErr == nil || observerErr.Code != browserprotocol.OpsObserverRunNotActive {
		t.Fatalf("inactive observation error = %v", observerErr)
	}
	response, observerErr := stream.Observe(ctx, browserprotocol.OpsObserverFrameOperation)
	if observerErr != nil || response.Status != "ok" ||
		response.Observation.FrameSequence != 1 {
		t.Fatalf("observation after the action gap = %#v, %v", response, observerErr)
	}

	_ = stream.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func newOpsObserverTestStream(
	t *testing.T,
	ctx context.Context,
	socketPath string,
	credentialFile string,
) *browserclient.OpsObserverStream {
	t.Helper()
	stream, err := browserclient.NewOpsObserverStream(ctx, browserclient.OpsObserverConfig{
		SocketPath: socketPath, CredentialFile: credentialFile,
		RunID: "11111111-1111-4111-8111-111111111111", TTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

func observerLeaseRequest(leaseID, runID string, expires time.Time) browserprotocol.OpsObserverRequest {
	return browserprotocol.OpsObserverRequest{
		ObserverLeaseID: leaseID,
		RunID:           runID,
		LeaseExpiresAt:  expires,
	}
}

// Two listeners front one Runtime. If each kept its own lease pointer both would
// be admitted and the single-observer guarantee would vanish, so this asserts the
// contention in both directions rather than only the local-Viewer-first case.
func TestOpsObserverLeaseManagerIsSharedAcrossListeners(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	expires := now.Add(10 * time.Minute)
	runID := "11111111-1111-4111-8111-111111111111"

	for _, order := range []struct {
		name         string
		firstLeaseID string
		laterLeaseID string
	}{
		{name: "local Viewer first", firstLeaseID: "lease-local", laterLeaseID: "lease-core"},
		{name: "Core Observer first", firstLeaseID: "lease-core", laterLeaseID: "lease-local"},
	} {
		t.Run(order.name, func(t *testing.T) {
			manager := NewOpsObserverLeaseManager(func() time.Time { return now })

			if failure := manager.Admit(observerLeaseRequest(order.firstLeaseID, runID, expires)); failure != nil {
				t.Fatalf("first listener was refused: %v", failure)
			}
			second := manager.Admit(observerLeaseRequest(order.laterLeaseID, runID, expires))
			if second == nil {
				t.Fatal("both listeners held the Runtime lease at the same time")
			}
			if second.Code != browserprotocol.OpsObserverAlreadyActive {
				t.Fatalf("contention code = %v, want %v", second.Code, browserprotocol.OpsObserverAlreadyActive)
			}

			// A release from the listener that does not own the lease must not
			// free it for anyone else.
			manager.Release(order.laterLeaseID)
			if !manager.held() {
				t.Fatal("a non-owner release dropped the active lease")
			}

			manager.Release(order.firstLeaseID)
			if manager.held() {
				t.Fatal("the owner release did not drop the lease")
			}
			if failure := manager.Admit(observerLeaseRequest(order.laterLeaseID, runID, expires)); failure != nil {
				t.Fatalf("second listener was refused after release: %v", failure)
			}
		})
	}
}

// Two servers built from the same manager must contend; two servers built
// without one must not, so the single-socket deployment is unchanged.
func TestOpsObserverServersShareOrIsolateLeasesByOption(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	expires := now.Add(10 * time.Minute)
	runID := "22222222-2222-4222-8222-222222222222"
	request := observerLeaseRequest("lease-a", runID, expires)

	shared := NewOpsObserverLeaseManager(func() time.Time { return now })
	first := newObserverTestServer(t, shared)
	second := newObserverTestServer(t, shared)
	if failure := first.admitLease(request); failure != nil {
		t.Fatalf("shared first admit: %v", failure)
	}
	if failure := second.admitLease(observerLeaseRequest("lease-b", runID, expires)); failure == nil {
		t.Fatal("servers sharing a manager both admitted a lease")
	}

	isolatedFirst := newObserverTestServer(t, nil)
	isolatedSecond := newObserverTestServer(t, nil)
	if failure := isolatedFirst.admitLease(request); failure != nil {
		t.Fatalf("isolated first admit: %v", failure)
	}
	if failure := isolatedSecond.admitLease(request); failure != nil {
		t.Fatalf("a server with its own manager must stay independent: %v", failure)
	}
}

func newObserverTestServer(t *testing.T, leases *OpsObserverLeaseManager) *OpsObserverServer {
	t.Helper()
	server, err := NewOpsObserverServer(OpsObserverServerOptions{
		SocketPath:        filepath.Join(t.TempDir(), "observer.sock"),
		ChannelCredential: strings.Repeat("c", 64),
		Observer:          &opsObserverTestEngine{},
		Leases:            leases,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

// The probe exists so a health check can prove the listener serves its protocol.
// If it claimed the lease it would evict the real observer on every interval, so
// that property is asserted directly rather than assumed from the code path.
func TestOpsObserverProbeNeverTakesTheLease(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	manager := NewOpsObserverLeaseManager(func() time.Time { return now })
	server := newObserverTestServer(t, manager)

	probe := browserprotocol.OpsObserverRequest{
		ContractID:        browserprotocol.OpsObserverContractID,
		ChannelCredential: strings.Repeat("c", 64),
		RequestID:         "11111111-1111-4111-8111-111111111111",
		Operation:         browserprotocol.OpsObserverProbeOperation,
		Deadline:          now.Add(time.Second),
	}
	if failure := probe.Validate(now); failure != nil {
		t.Fatalf("probe request rejected: %v", failure)
	}
	if manager.held() {
		t.Fatal("a lease existed before any observer claimed one")
	}

	// A real observer takes the lease; the probe must still validate, because a
	// health check has to keep working while someone is observing.
	observed := observerLeaseRequest(
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		now.Add(10*time.Minute),
	)
	if failure := server.admitLease(observed); failure != nil {
		t.Fatalf("observer admit: %v", failure)
	}
	if failure := probe.Validate(now); failure != nil {
		t.Fatalf("probe rejected while an observer held the lease: %v", failure)
	}
	if !manager.held() {
		t.Fatal("the observer lease disappeared")
	}
}

// A probe carries no lease, so requests that smuggle lease fields into it must be
// refused rather than silently treated as an observation.
func TestOpsObserverProbeRejectsLeaseFieldsAndReturnsNoContent(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	base := browserprotocol.OpsObserverRequest{
		ContractID:        browserprotocol.OpsObserverContractID,
		ChannelCredential: strings.Repeat("c", 64),
		RequestID:         "11111111-1111-4111-8111-111111111111",
		Operation:         browserprotocol.OpsObserverProbeOperation,
		Deadline:          now.Add(time.Second),
	}
	for name, mutate := range map[string]func(*browserprotocol.OpsObserverRequest){
		"lease id": func(r *browserprotocol.OpsObserverRequest) {
			r.ObserverLeaseID = "22222222-2222-4222-8222-222222222222"
		},
		"run id": func(r *browserprotocol.OpsObserverRequest) {
			r.RunID = "33333333-3333-4333-8333-333333333333"
		},
		"lease expiry": func(r *browserprotocol.OpsObserverRequest) {
			r.LeaseExpiresAt = now.Add(time.Minute)
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			if request.Validate(now) == nil {
				t.Fatalf("a probe carrying a %s was accepted", name)
			}
		})
	}

	response := browserprotocol.OpsObserverProbeResponse(base.RequestID)
	if response.Observation != nil {
		t.Fatal("the probe response carried an observation")
	}
	if response.Status != "ok" || response.RequestID != base.RequestID {
		t.Fatalf("probe response = %#v", response)
	}
}
