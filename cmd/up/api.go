package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/giulianotesta7/up/internal/remote"
	"github.com/giulianotesta7/up/internal/tui"
)

func startAPI(ctx context.Context, cancel context.CancelFunc, addr, cert, key, tokenPath string, stderr io.Writer, s tui.Store) (<-chan error, error) {
	_, err := remote.LoadVerifier(tokenPath)
	missing := errors.Is(err, remote.ErrNoToken)
	if err != nil && !missing {
		return nil, err
	}
	if cert != "" {
		if _, err := tls.LoadX509KeyPair(cert, key); err != nil {
			return nil, err
		}
	}
	// Bind before creating the token, so a failed start leaves no token behind.
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	if missing {
		token, err := remote.GenerateToken()
		if err == nil {
			err = remote.SaveToken(tokenPath, token)
		}
		if err != nil {
			listener.Close()
			return nil, err
		}
		fmt.Fprintf(stderr, "up: access token (shown once): %s\n", token)
	}
	fmt.Fprintf(stderr, "up: API listening on %s\n", listener.Addr())
	routes := http.NewServeMux()
	routes.Handle("/api/v1/snapshot", remote.SnapshotRoutes(func(ctx context.Context, now time.Time) (tui.Snapshot, error) {
		return tui.Load(ctx, s, now)
	}))
	writes := remote.WriteRoutes(s)
	routes.Handle("/api/v1/checks", writes)
	routes.Handle("/api/v1/checks/", writes)
	alerts := remote.AlertWriteRoutes(s)
	for _, path := range []string{"/api/v1/smtp-servers", "/api/v1/smtp-servers/", "/api/v1/channels", "/api/v1/channels/"} {
		routes.Handle(path, alerts)
	}
	// Wildcard routes are more specific than the prefixes above and win.
	if ms, ok := s.(remote.MaintenanceStore); ok {
		maintenance := remote.MaintenanceRoutes(ms)
		routes.Handle("/api/v1/checks/{name}/maintenance", maintenance)
		routes.Handle("/api/v1/checks/{name}/maintenance/{id}", maintenance)
	}
	if ds, ok := s.(remote.DefaultsStore); ok {
		routes.Handle("/api/v1/defaults", remote.DefaultsRoutes(ds))
	}
	if ts, ok := s.(remote.ChannelTestStore); ok {
		routes.Handle("/api/v1/channels/{name}/test", remote.ChannelTestRoutes(ts))
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifier, err := remote.LoadVerifier(tokenPath)
		if err != nil {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		remote.NewHandler(remote.Options{Verifier: verifier, Routes: routes}).ServeHTTP(w, r)
	})
	server := remote.NewHTTPServer(addr, handler)
	done := make(chan error, 1)
	go func() {
		stopped := make(chan struct{})
		shutdownDone := make(chan struct{})
		go func() {
			defer close(shutdownDone)
			select {
			case <-ctx.Done():
				shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				_ = server.Shutdown(shutdown)
			case <-stopped:
			}
		}()
		var err error
		if cert != "" {
			err = server.ServeTLS(listener, cert, key)
		} else {
			err = server.Serve(listener)
		}
		close(stopped)
		<-shutdownDone
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		if err != nil {
			cancel()
		}
		done <- err
		close(done)
	}()
	return done, nil
}
