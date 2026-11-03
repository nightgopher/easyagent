// easyagent-relay forwards opaque TLS streams; it does not run agents or store conversations.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nightgopher/easyagent/internal/relay"
)

var version = "development"

func main() {
	addr := flag.String("listen", "127.0.0.1:8787", "listen address behind an HTTPS reverse proxy")
	state := flag.String("state", "/var/lib/easyagent-relay/routes.json", "private registration store")
	flag.Parse()
	b, err := relay.NewBroker(relay.BrokerConfig{StatePath: *state, RegistrationKey: os.Getenv("EA_RELAY_REGISTRATION_KEY"), Version: version})
	if err != nil {
		slog.Error("relay configuration rejected", "error", err)
		os.Exit(1)
	}
	defer b.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s := &http.Server{Addr: *addr, Handler: b.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		<-ctx.Done()
		_ = b.Close()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Shutdown(shutdown)
	}()
	slog.Info("relay listening", "address", *addr)
	if err = s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("relay stopped", "error", err)
		os.Exit(1)
	}
}
