// Package events implements an at-least-once outbox publisher and idempotent consumer.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	amqp "github.com/rabbitmq/amqp091-go"
	"net"
	"net/url"
	"securedesk/internal/platform"
	"strings"
	"time"
)

type Event struct {
	ID     string `json:"id"`
	Owner  string `json:"owner_id"`
	Ticket string `json:"ticket_id"`
	Action string `json:"action"`
}

func Connect() (*amqp.Connection, *amqp.Channel, error) {
	for _, host := range strings.Split(platform.Env("MQ_HOSTS", "172.29.40.11,172.29.40.12,172.29.40.13"), ",") {
		u := url.URL{Scheme: "amqp", User: url.UserPassword("desk", platform.Secret("MQ_PASSWORD")), Host: net.JoinHostPort(host, "5672"), Path: "/"}
		c, e := amqp.DialConfig(u.String(), amqp.Config{Heartbeat: 10 * time.Second, Dial: amqp.DefaultDial(2 * time.Second)})
		if e != nil {
			continue
		}
		ch, e := c.Channel()
		if e == nil {
			_, e = ch.QueueDeclare("desk.events", true, false, false, false, amqp.Table{"x-queue-type": "quorum", "x-quorum-initial-group-size": int32(3)})
		}
		if e == nil {
			e = ch.Confirm(false)
		}
		if e == nil {
			e = ch.Qos(20, 0, false)
		}
		if e == nil {
			return c, ch, nil
		}
		_ = c.Close()
	}
	return nil, nil, errors.New("no broker available")
}
func Publish(ctx context.Context, ch *amqp.Channel, returns <-chan amqp.Return, event Event) error {
	data, e := json.Marshal(event)
	if e != nil {
		return e
	}
	confirmation, e := ch.PublishWithDeferredConfirmWithContext(ctx, "", "desk.events", true, false, amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: event.ID, Body: data})
	if e != nil {
		return e
	}
	ack, e := confirmation.WaitContext(ctx)
	if e != nil {
		return e
	}
	if !ack {
		return errors.New("broker rejected publication")
	}
	select {
	case <-returns:
		return errors.New("unroutable publication")
	default:
		return nil
	}
}
func RunOnce(ctx context.Context, ch *amqp.Channel, returns <-chan amqp.Return) error {
	c, e := platform.DB(ctx)
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	tx, e := c.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	rows, e := tx.Query(ctx, `SELECT id::text,owner_id::text,ticket_id::text,action FROM outbox WHERE published_at IS NULL ORDER BY created_at LIMIT 20 FOR UPDATE SKIP LOCKED`)
	if e != nil {
		return e
	}
	events := []Event{}
	for rows.Next() {
		var event Event
		if e = rows.Scan(&event.ID, &event.Owner, &event.Ticket, &event.Action); e != nil {
			rows.Close()
			return e
		}
		events = append(events, event)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	for _, event := range events {
		if e = Publish(ctx, ch, returns, event); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE outbox SET published_at=now() WHERE id=$1`, event.ID); e != nil {
			return e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	for i := 0; i < 20; i++ {
		delivery, ok, err := ch.Get("desk.events", false)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		var event Event
		if err = json.Unmarshal(delivery.Body, &event); err != nil {
			return err
		}
		if _, err = c.Exec(ctx, `INSERT INTO audit(event_id,owner_id,ticket_id,action) VALUES ($1,$2,$3,$4) ON CONFLICT(event_id) DO NOTHING`, event.ID, event.Owner, event.Ticket, event.Action); err != nil {
			return err
		}
		if err = delivery.Ack(false); err != nil {
			return err
		}
	}
	return nil
}

// Duplicate verifies the externally visible exactly-once effect of redelivery.
func Duplicate(ctx context.Context, ticket string) error {
	c, e := platform.DB(ctx)
	if e != nil {
		return e
	}
	defer c.Close(context.Background())
	var event Event
	e = c.QueryRow(ctx, `SELECT id::text,owner_id::text,ticket_id::text,action FROM outbox WHERE ticket_id=$1 ORDER BY created_at LIMIT 1`, ticket).Scan(&event.ID, &event.Owner, &event.Ticket, &event.Action)
	if e != nil {
		return e
	}
	conn, ch, e := Connect()
	if e != nil {
		return e
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	returns := ch.NotifyReturn(make(chan amqp.Return, 10))
	for i := 0; i < 3; i++ {
		if e = Publish(ctx, ch, returns, event); e != nil {
			return e
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(3 * time.Second):
	}
	var n int
	if e = c.QueryRow(ctx, `SELECT count(*) FROM audit WHERE event_id=$1`, event.ID).Scan(&n); e != nil {
		return e
	}
	if n != 1 {
		return fmt.Errorf("expected one audit row, got %d", n)
	}
	return nil
}
