package main

import (
	"context"
	amqp "github.com/rabbitmq/amqp091-go"
	"log/slog"
	"os/signal"
	"securedesk/internal/events"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	for ctx.Err() == nil {
		conn, ch, e := events.Connect()
		if e == nil {
			slog.Info("broker_connected")
			returns := ch.NotifyReturn(make(chan amqp.Return, 20))
			for ctx.Err() == nil {
				step, cancel := context.WithTimeout(ctx, 12*time.Second)
				abort := context.AfterFunc(step, func() { _ = conn.Close() })
				e = events.RunOnce(step, ch, returns)
				abort()
				cancel()
				if e != nil {
					break
				}
				select {
				case <-ctx.Done():
				case <-time.After(300 * time.Millisecond):
				}
			}
			_ = conn.Close()
		}
		if ctx.Err() != nil {
			return
		}
		slog.Warn("worker_retry")
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}
