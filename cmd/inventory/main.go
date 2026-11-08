package main

import (
	"context"
	inventoryv1 "flashcart/api/gen/inventory/v1"
	"flashcart/internal/inventory"
	"flashcart/internal/platform"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"log/slog"
	"net"
	"os"
	"time"
)

func main() {
	if e := run(); e != nil {
		slog.Error("inventory stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := platform.Root()
	defer cancel()
	db, e := platform.OpenDB(ctx, platform.MustEnv("DATABASE_URL"), "inventory")
	if e != nil {
		return e
	}
	defer db.Close()
	key, e := platform.ReadPublic(platform.MustEnv("JWT_PUBLIC_KEY"))
	if e != nil {
		return e
	}
	service := &inventory.Service{DB: db, PublicKey: key}
	go platform.Relay(ctx, db)
	go platform.Consume(ctx, "inventory-products-v1", service.HandleProduct)
	tls, e := platform.MutualTLS(true)
	if e != nil {
		return e
	}
	listener, e := net.Listen("tcp", ":9090")
	if e != nil {
		return e
	}
	defer listener.Close()
	authorize := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		p, ok := peer.FromContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing peer")
		}
		t, ok := p.AuthInfo.(credentials.TLSInfo)
		allowed := false
		if ok && len(t.State.PeerCertificates) > 0 {
			for _, uri := range t.State.PeerCertificates[0].URIs {
				if uri.String() == "spiffe://flashcart/orders" {
					allowed = true
				}
			}
		}
		if !allowed {
			return nil, status.Error(codes.PermissionDenied, "orders identity required")
		}
		return handler(ctx, req)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(tls)), grpc.UnaryInterceptor(authorize), grpc.MaxRecvMsgSize(64<<10), grpc.MaxConcurrentStreams(64))
	inventoryv1.RegisterInventoryServiceServer(server, service)
	fail := make(chan error, 1)
	go func() { fail <- server.Serve(listener) }()
	go func() {
		select {
		case <-ctx.Done():
		case err := <-fail:
			slog.Error("grpc stopped", "error", err)
			cancel()
		}
		done := make(chan struct{})
		go func() { server.GracefulStop(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			server.Stop()
		}
	}()
	return platform.Serve(ctx, "inventory", service.Routes())
}
