package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"securedesk/internal/httpapi"
	"securedesk/internal/platform"
	"syscall"
	"time"
)

func main() {
	cache := platform.Redis()
	defer cache.Close()
	server := &http.Server{Addr: ":8000", Handler: httpapi.New(cache).Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		<-ctx.Done()
		deadline, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = server.Shutdown(deadline)
	}()
	slog.Info("SecureDesk Go API listening", "port", 8000)
	if e := server.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
		slog.Error("http_server_failed")
		os.Exit(1)
	}
}
