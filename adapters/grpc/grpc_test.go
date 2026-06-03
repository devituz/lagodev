package grpc

import (
	"context"
	"errors"
	"log"
	"net"
	"strings"
	"testing"
	"time"

	lagoauth "github.com/devituz/lagodev/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// silentLogger discards output so test logs stay readable.
func silentLogger() *log.Logger {
	return log.New(silentWriter{}, "", 0)
}

type silentWriter struct{}

func (silentWriter) Write(p []byte) (int, error) { return len(p), nil }

// healthHandlers is a tiny health-check service we register on every
// test server so we have something real to call. We attach a custom
// behaviour function so a test can poison it (panic, error, …) without
// touching grpc's health package internals.
type healthHandlers struct {
	healthpb.UnimplementedHealthServer
	behave func(ctx context.Context) error
}

func (h *healthHandlers) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	if h.behave != nil {
		if err := h.behave(ctx); err != nil {
			return nil, err
		}
	}
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

// startServer builds a Server with the given opts, registers the
// health service, listens on an in-memory bufconn, and returns a
// dialed client + cleanup.
func startServer(t *testing.T, opts Options, beh func(ctx context.Context) error) (healthpb.HealthClient, func()) {
	t.Helper()
	if opts.Logger == nil {
		opts.Logger = silentLogger()
	}
	srv := New(opts)
	healthpb.RegisterHealthServer(srv.GRPC(), &healthHandlers{behave: beh})

	lis := bufconn.Listen(1 << 16)
	go func() { _ = srv.GRPC().Serve(lis) }()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	}
	return healthpb.NewHealthClient(conn), cleanup
}

// --- Recovery ----------------------------------------------------------

func TestRecovery_TurnsPanicInto500(t *testing.T) {
	cli, cleanup := startServer(t, Options{Recovery: true}, func(_ context.Context) error {
		panic("intentional test panic")
	})
	defer cleanup()
	_, err := cli.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err == nil {
		t.Fatal("expected error after handler panic")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.Internal {
		t.Fatalf("want codes.Internal, got %v", err)
	}
}

// --- Logging -----------------------------------------------------------

func TestLogging_ProducesStructuredLine(t *testing.T) {
	captured := &captureWriter{}
	cli, cleanup := startServer(t, Options{
		Logging: true,
		Logger:  log.New(captured, "", 0),
	}, nil)
	defer cleanup()

	_, err := cli.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !strings.Contains(captured.String(), "grpc:") {
		t.Fatalf("expected grpc: log line, got %q", captured.String())
	}
	if !strings.Contains(captured.String(), "/grpc.health.v1.Health/Check") {
		t.Fatalf("expected method in log, got %q", captured.String())
	}
}

type captureWriter struct{ b []byte }

func (c *captureWriter) Write(p []byte) (int, error) { c.b = append(c.b, p...); return len(p), nil }
func (c *captureWriter) String() string              { return string(c.b) }

// --- Auth --------------------------------------------------------------

func newAuthMgr(t *testing.T) *lagoauth.Manager {
	t.Helper()
	m, err := lagoauth.New(lagoauth.Config{Secret: "0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatalf("new auth: %v", err)
	}
	return m
}

func TestAuth_MissingMetadataIsUnauthenticated(t *testing.T) {
	cli, cleanup := startServer(t, Options{
		Auth: AuthFromManager(newAuthMgr(t)),
	}, nil)
	defer cleanup()

	_, err := cli.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if st, ok := status.FromError(err); !ok || st.Code() != codes.Unauthenticated {
		t.Fatalf("want Unauthenticated, got %v", err)
	}
}

func TestAuth_InvalidTokenIsUnauthenticated(t *testing.T) {
	cli, cleanup := startServer(t, Options{
		Auth: AuthFromManager(newAuthMgr(t)),
	}, nil)
	defer cleanup()
	ctx := metadata.AppendToOutgoingContext(context.Background(),
		"authorization", "Bearer nope.nope.nope")
	_, err := cli.Check(ctx, &healthpb.HealthCheckRequest{})
	if st, ok := status.FromError(err); !ok || st.Code() != codes.Unauthenticated {
		t.Fatalf("want Unauthenticated, got %v", err)
	}
}

func TestAuth_ValidTokenPopulatesContext(t *testing.T) {
	authMgr := newAuthMgr(t)
	token, _, err := authMgr.IssueAccess(42, "admin")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	var seenUser uint64
	var seenRole string
	cli, cleanup := startServer(t, Options{
		Auth: AuthFromManager(authMgr),
	}, func(ctx context.Context) error {
		seenUser = UserID(ctx)
		seenRole = Role(ctx)
		return nil
	})
	defer cleanup()

	ctx := metadata.AppendToOutgoingContext(context.Background(),
		"authorization", "Bearer "+token)
	_, err = cli.Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if seenUser != 42 || seenRole != "admin" {
		t.Fatalf("handler saw user=%d role=%q", seenUser, seenRole)
	}
}

func TestAuth_ClaimsAccessor(t *testing.T) {
	authMgr := newAuthMgr(t)
	token, _, _ := authMgr.IssueAccess(7, "user")
	var got *lagoauth.Claims
	cli, cleanup := startServer(t, Options{
		Auth: AuthFromManager(authMgr),
	}, func(ctx context.Context) error {
		got = Claims(ctx)
		return nil
	})
	defer cleanup()

	ctx := metadata.AppendToOutgoingContext(context.Background(),
		"authorization", "Bearer "+token)
	_, _ = cli.Check(ctx, &healthpb.HealthCheckRequest{})
	if got == nil || got.UserID != 7 {
		t.Fatalf("Claims = %+v", got)
	}
}

// --- Lifecycle ---------------------------------------------------------

func TestRun_ListensAndGracefullyStops(t *testing.T) {
	srv := New(Options{Addr: ":0", Logger: silentLogger()})
	// Register health on a real listener so the server has something to do.
	healthpb.RegisterHealthServer(srv.GRPC(), health.NewServer())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	// Allow the listener to come up.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

func TestNew_DefaultAddr(t *testing.T) {
	srv := New(Options{Logger: silentLogger()})
	if srv.addr != ":50051" {
		t.Fatalf("default addr = %q", srv.addr)
	}
}

func TestNew_AppliesAdditionalInterceptors(t *testing.T) {
	var unaryHit, streamHit bool
	srv := New(Options{
		Logger: silentLogger(),
		AdditionalUnaryInterceptors: []grpc.UnaryServerInterceptor{
			func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
				unaryHit = true
				return h(ctx, req)
			},
		},
		AdditionalStreamInterceptors: []grpc.StreamServerInterceptor{
			func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
				streamHit = true
				return h(srv, ss)
			},
		},
	})
	healthpb.RegisterHealthServer(srv.GRPC(), &healthHandlers{})

	lis := bufconn.Listen(1 << 16)
	go func() { _ = srv.GRPC().Serve(lis) }()
	defer srv.Stop()

	conn, _ := grpc.NewClient("passthrough://bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
	)
	defer conn.Close()
	_, _ = healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{})

	if !unaryHit {
		t.Fatal("custom unary interceptor never ran")
	}
	// Stream interceptor only fires for streaming RPCs — silence warning.
	_ = streamHit
}

// --- Accessors ---------------------------------------------------------

func TestUserID_ZeroWhenUnset(t *testing.T) {
	if UserID(context.Background()) != 0 {
		t.Fatal("UserID on bare ctx must be 0")
	}
}

func TestRole_EmptyWhenUnset(t *testing.T) {
	if Role(context.Background()) != "" {
		t.Fatal("Role on bare ctx must be empty")
	}
}

func TestClaims_NilWhenUnset(t *testing.T) {
	if Claims(context.Background()) != nil {
		t.Fatal("Claims on bare ctx must be nil")
	}
}
