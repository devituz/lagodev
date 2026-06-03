// Package grpc provides a lagodev-flavoured gRPC server: Laravel-style
// graceful shutdown, structured logging, panic recovery, and an
// optional JWT auth interceptor that surfaces `auth_user_id` and
// `auth_role` on the request context — the same contract the lagodev
// web middleware uses.
//
// The adapter is a separate Go module so the core lagodev module does
// not depend on google.golang.org/grpc:
//
//	go get github.com/devituz/lagodev/adapters/grpc@latest
//
// Usage:
//
//	srv := lagogrpc.New(lagogrpc.Options{
//	    Addr:    ":50051",
//	    Auth:    lagogrpc.AuthFromManager(authMgr),
//	    Logger:  log.Default(),
//	})
//	mypb.RegisterMyServiceServer(srv.GRPC(), impl)
//	if err := srv.Run(ctx); err != nil { ... }
package grpc

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	lagoauth "github.com/devituz/lagodev/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Server is the lagodev-flavoured gRPC server.
type Server struct {
	addr    string
	logger  *log.Logger
	timeout time.Duration
	grpc    *grpc.Server
	mu      sync.Mutex
	running bool
}

// Options configures Server.
type Options struct {
	// Addr to listen on (e.g. ":50051").
	Addr string
	// Logger receives access / panic logs. Default: log.Default().
	Logger *log.Logger
	// ShutdownTimeout caps GracefulStop. Default 10s.
	ShutdownTimeout time.Duration
	// Auth optionally enforces a JWT on every RPC. When non-nil it is
	// installed as a unary interceptor; pair with AuthFromManager to
	// reuse the lagodev/auth manager you already configured for HTTP.
	Auth AuthFunc
	// Recovery toggles panic recovery (default true).
	Recovery bool
	// Logging toggles access logging (default true).
	Logging bool
	// AdditionalUnaryInterceptors are appended after Recovery/Auth/
	// Logging so call sites can add tracing or rate-limit middleware.
	AdditionalUnaryInterceptors []grpc.UnaryServerInterceptor
	// AdditionalStreamInterceptors are appended after Recovery/Auth/
	// Logging in the streaming chain.
	AdditionalStreamInterceptors []grpc.StreamServerInterceptor
	// AdditionalOptions are appended to the underlying grpc.Server
	// construction (e.g. TLS credentials).
	AdditionalOptions []grpc.ServerOption
}

// AuthFunc resolves the metadata-carried token to a claims-bearing
// context. Returning a non-nil error short-circuits the RPC with
// codes.Unauthenticated.
type AuthFunc func(ctx context.Context, token string) (context.Context, error)

// AuthFromManager builds an AuthFunc using a lagodev/auth Manager. On
// success it populates "auth_user_id" and "auth_role" in the context
// so service handlers can pull them via FromContext.
func AuthFromManager(m *lagoauth.Manager) AuthFunc {
	return func(ctx context.Context, token string) (context.Context, error) {
		claims, err := m.Parse(token)
		if err != nil {
			return ctx, err
		}
		ctx = context.WithValue(ctx, userIDKey{}, claims.UserID)
		ctx = context.WithValue(ctx, roleKey{}, claims.Role)
		ctx = context.WithValue(ctx, claimsKey{}, claims)
		return ctx, nil
	}
}

// UserID returns the authenticated user ID set by AuthFromManager
// (zero when not set).
func UserID(ctx context.Context) uint64 {
	v, _ := ctx.Value(userIDKey{}).(uint64)
	return v
}

// Role returns the authenticated role set by AuthFromManager.
func Role(ctx context.Context) string {
	v, _ := ctx.Value(roleKey{}).(string)
	return v
}

// Claims returns the parsed *auth.Claims set by AuthFromManager.
func Claims(ctx context.Context) *lagoauth.Claims {
	v, _ := ctx.Value(claimsKey{}).(*lagoauth.Claims)
	return v
}

type (
	userIDKey struct{}
	roleKey   struct{}
	claimsKey struct{}
)

// New constructs a Server. Interceptors are installed in the order:
// Recovery → Auth → Logging → user-supplied.
func New(opts Options) *Server {
	if opts.Addr == "" {
		opts.Addr = ":50051"
	}
	if opts.Logger == nil {
		opts.Logger = log.Default()
	}
	if opts.ShutdownTimeout == 0 {
		opts.ShutdownTimeout = 10 * time.Second
	}
	// Default-on for both Recovery and Logging unless explicitly disabled
	// by the caller. We can't distinguish "user set false" from "zero
	// value" with a bool, so we model both as default-on: callers wanting
	// to disable wire in their own interceptors directly with an empty
	// AdditionalOptions list. (Pre-1.0 — open to feedback.)
	if !opts.Recovery && len(opts.AdditionalUnaryInterceptors) == 0 && len(opts.AdditionalStreamInterceptors) == 0 {
		opts.Recovery = true
	}
	if !opts.Logging && len(opts.AdditionalUnaryInterceptors) == 0 && len(opts.AdditionalStreamInterceptors) == 0 {
		opts.Logging = true
	}

	var unary []grpc.UnaryServerInterceptor
	var stream []grpc.StreamServerInterceptor
	if opts.Recovery {
		unary = append(unary, recoveryUnary(opts.Logger))
		stream = append(stream, recoveryStream(opts.Logger))
	}
	if opts.Auth != nil {
		unary = append(unary, authUnary(opts.Auth))
		stream = append(stream, authStream(opts.Auth))
	}
	if opts.Logging {
		unary = append(unary, loggingUnary(opts.Logger))
		stream = append(stream, loggingStream(opts.Logger))
	}
	unary = append(unary, opts.AdditionalUnaryInterceptors...)
	stream = append(stream, opts.AdditionalStreamInterceptors...)

	serverOpts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(unary...),
		grpc.ChainStreamInterceptor(stream...),
	}
	serverOpts = append(serverOpts, opts.AdditionalOptions...)

	return &Server{
		addr:    opts.Addr,
		logger:  opts.Logger,
		timeout: opts.ShutdownTimeout,
		grpc:    grpc.NewServer(serverOpts...),
	}
}

// GRPC returns the underlying *grpc.Server so callers can RegisterX().
func (s *Server) GRPC() *grpc.Server { return s.grpc }

// Run starts the listener and blocks until ctx is cancelled or a
// SIGINT/SIGTERM is received. GracefulStop is called on shutdown.
func (s *Server) Run(ctx context.Context) error {
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()

	l, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("grpc: listen %s: %w", s.addr, err)
	}
	s.logger.Printf("grpc: serving %s", s.addr)

	errCh := make(chan error, 1)
	go func() {
		if err := s.grpc.Serve(l); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			errCh <- err
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.logger.Printf("grpc: ctx cancelled, shutting down")
	case <-sig:
		s.logger.Printf("grpc: signal received, shutting down")
	}

	done := make(chan struct{})
	go func() {
		s.grpc.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(s.timeout):
		s.logger.Printf("grpc: graceful stop timed out, forcing")
		s.grpc.Stop()
	}
	return nil
}

// Stop terminates the server immediately. Use for tests; production
// callers should rely on Run's graceful shutdown.
func (s *Server) Stop() { s.grpc.Stop() }

// --- Interceptors ------------------------------------------------------

func recoveryUnary(l *log.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				l.Printf("grpc: panic in %s: %v\n%s", info.FullMethod, r, debug.Stack())
				err = status.Errorf(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

func recoveryStream(l *log.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				l.Printf("grpc: stream panic in %s: %v\n%s", info.FullMethod, r, debug.Stack())
				err = status.Errorf(codes.Internal, "internal error")
			}
		}()
		return handler(srv, ss)
	}
}

func authUnary(fn AuthFunc) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, err := authenticate(ctx, fn)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

func authStream(fn AuthFunc) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := authenticate(ss.Context(), fn)
		if err != nil {
			return err
		}
		wrapped := &serverStream{ServerStream: ss, ctx: ctx}
		return handler(srv, wrapped)
	}
}

func authenticate(ctx context.Context, fn AuthFunc) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx, status.Errorf(codes.Unauthenticated, "missing metadata")
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return ctx, status.Errorf(codes.Unauthenticated, "missing authorization metadata")
	}
	tok := strings.TrimPrefix(values[0], "Bearer ")
	ctx, err := fn(ctx, tok)
	if err != nil {
		return ctx, status.Errorf(codes.Unauthenticated, "invalid token: %v", err)
	}
	return ctx, nil
}

func loggingUnary(l *log.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		l.Printf("grpc: %s %s %s", info.FullMethod, codeOf(err), time.Since(start).Round(time.Microsecond))
		return resp, err
	}
}

func loggingStream(l *log.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		l.Printf("grpc: stream %s %s %s", info.FullMethod, codeOf(err), time.Since(start).Round(time.Microsecond))
		return err
	}
}

func codeOf(err error) string {
	if err == nil {
		return "OK"
	}
	st, ok := status.FromError(err)
	if !ok {
		return "Unknown"
	}
	return st.Code().String()
}

// serverStream wraps grpc.ServerStream with a custom context so the
// auth interceptor's claim values reach the handler.
type serverStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *serverStream) Context() context.Context { return s.ctx }
