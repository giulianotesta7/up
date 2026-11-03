package remote

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"
)

// MaxBodyBytes caps every request body, declared or chunked.
const MaxBodyBytes = 1 << 20

// Options configures the API handler.
type Options struct {
	Verifier *Verifier
	Version  string
	// Routes serves every authenticated path except the built-in ping.
	Routes http.Handler
}

// NewHandler authenticates every request before routing, so an unauthenticated
// caller learns nothing about which paths exist.
func NewHandler(o Options) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		if !authorized(r, o.Verifier) {
			h.Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if r.ContentLength > MaxBodyBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/ping" {
			// Ping takes no body, but an oversized chunked one is still refused.
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				writeBodyError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"name": "up", "version": o.Version})
			return
		}
		if o.Routes == nil {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		o.Routes.ServeHTTP(w, r)
	})
}

// authorized accepts exactly one "Authorization: Bearer <token>" header. The
// token is never read from the URL, where logs and history would keep it.
func authorized(r *http.Request, v *Verifier) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || v == nil {
		return false
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return false
	}
	return v.Verify(token)
}

// DecodeJSON reads one JSON object into dst. On failure it has already
// answered the request and returns false: 415 for another content type, 413
// for an oversized body, 400 for unknown fields, trailing data or bad JSON.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
		return false
	}
	dec := json.NewDecoder(r.Body)
	var raw json.RawMessage
	err = dec.Decode(&raw)
	if err == nil && !bytes.HasPrefix(raw, []byte("{")) {
		err = errors.New("the request body must be a JSON object")
	}
	if err == nil {
		strict := json.NewDecoder(bytes.NewReader(raw))
		strict.DisallowUnknownFields()
		err = strict.Decode(dst)
	}
	if err == nil {
		// A size error found while looking for trailing data stays a size
		// error; anything else after the value is just unexpected data.
		if _, next := dec.Token(); next != io.EOF {
			err = next
			if err == nil {
				err = errors.New("unexpected data after the JSON value")
			}
		}
	}
	if err != nil {
		writeBodyError(w, err)
		return false
	}
	return true
}

// writeBodyError answers 413 for a body over the limit and 400 otherwise.
func writeBodyError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid request body")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// NewHTTPServer returns a server with every timeout and size limit set, so a
// slow or oversized client cannot hold resources, and TLS 1.2 or newer.
func NewHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

// ValidateListen refuses to serve the token over plain HTTP on a reachable
// address unless the operator chose it: TLS, a TLS-terminating proxy
// (behindProxy) or an explicit plain HTTP mode (noTLS). Loopback is always
// allowed.
func ValidateListen(addr string, hasTLS, behindProxy, noTLS bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid API address %q: %w", addr, err)
	}
	if hasTLS || behindProxy || noTLS {
		return nil
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("API address %q is reachable without TLS: add --tls-cert and --tls-key, use --behind-proxy behind a TLS proxy, use --no-tls to accept plain HTTP, or listen on 127.0.0.1", addr)
}
