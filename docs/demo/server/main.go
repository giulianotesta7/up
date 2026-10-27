// Command server provides controlled HTTP responses for the README demo.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "HTTP listen address")
	healthy := flag.Duration("payments-healthy", 0, "time payments returns 200 before failing permanently")
	flag.Parse()
	if *healthy < 0 {
		log.Fatal("payments-healthy must not be negative")
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	start := time.Now()
	rng := rand.New(rand.NewSource(12))
	var mu sync.Mutex
	searchRequests := 0
	mux := http.NewServeMux()
	for _, path := range []string{"/api", "/search", "/docs", "/payments", "/auth", "/cdn", "/mail", "/queue"} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			delay := time.Duration(75+rng.Intn(11)) * time.Millisecond
			switch r.URL.Path {
			case "/search":
				searchRequests++
				delay = time.Duration(110+rng.Intn(21)) * time.Millisecond
				if searchRequests%6 == 0 {
					delay = 600 * time.Millisecond
				}
			case "/docs":
				delay = 400 * time.Millisecond
			case "/auth":
				delay = time.Duration(38+rng.Intn(25)) * time.Millisecond
			case "/cdn":
				delay = time.Duration(18+rng.Intn(17)) * time.Millisecond
			case "/mail":
				delay = time.Duration(180+rng.Intn(81)) * time.Millisecond
			case "/queue":
				delay = time.Duration(92+rng.Intn(9)) * time.Millisecond
			}
			mu.Unlock()
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-r.Context().Done():
				return
			case <-timer.C:
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			if r.URL.Path == "/payments" && time.Since(start) >= *healthy {
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprintln(w, "payments temporarily unavailable")
				return
			}
			fmt.Fprintln(w, "ok")
		})
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			log.Print(err)
		}
	}()
	log.Printf("demo server listening on %s", listener.Addr())
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-done
}
