package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/remote"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/tui"
)

// apiRecorder records the API the daemon serves. *store.Store implements it.
type apiRecorder interface {
	SetAPIListener(context.Context, config.APIListener) error
}

// startAPI serves the API on addr. secretKey opens heartbeat ping tokens
// for the authenticated reveal route and new telegram bot tokens to check
// their format.
func startAPI(ctx context.Context, cancel context.CancelFunc, addr, cert, key, tokenPath string, stderr io.Writer, s tui.Store, secretKey *secret.Key) (<-chan error, error) {
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
	// The TUI builds heartbeat ping URLs from the bound address.
	if r, ok := s.(apiRecorder); ok {
		if err := r.SetAPIListener(ctx, config.APIListener{Address: listener.Addr().String(), TLS: cert != ""}); err != nil {
			listener.Close()
			return nil, err
		}
	}
	fmt.Fprintf(stderr, "up: API listening on %s\n", listener.Addr())
	routes := http.NewServeMux()
	routes.Handle("/api/v1/snapshot", remote.SnapshotRoutes(func(ctx context.Context, now time.Time) (tui.Snapshot, error) {
		return tui.Load(ctx, s, now)
	}))
	writes := remote.WriteRoutes(s)
	routes.Handle("/api/v1/checks", writes)
	routes.Handle("/api/v1/checks/", writes)
	alerts := remote.AlertWriteRoutes(s, secretKey)
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
	if dr, ok := s.(remote.DeliveryReader); ok {
		routes.Handle("/api/v1/deliveries", remote.DeliveryRoutes(dr))
	}
	if ts, ok := s.(remote.ChannelTestStore); ok {
		routes.Handle("/api/v1/channels/{name}/test", remote.ChannelTestRoutes(ts))
	}
	if ts, ok := s.(remote.HeartbeatTokenStore); ok {
		routes.Handle("/api/v1/checks/{name}/heartbeat-token", remote.HeartbeatTokenRoutes(ts, secretKey))
	}
	// Heartbeat pings carry their own token in the path and skip the access
	// token, which never belongs in a crontab.
	var pings http.Handler
	if ps, ok := s.(remote.PingStore); ok {
		pings = remote.HeartbeatRoutes(ps, remote.PingOptions{Limits: remote.DefaultPingLimits})
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pings != nil && strings.HasPrefix(r.URL.Path, remote.HeartbeatPrefix) {
			pings.ServeHTTP(w, r)
			return
		}
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
