// Package queue provides RabbitMQ publishing for the veloci-api service.
package queue

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/cenkalti/backoff/v4"
	amqp "github.com/rabbitmq/amqp091-go"
)

// QueueName is the durable RabbitMQ queue used for veloci job messages.
const QueueName = "veloci.jobs"

// Job represents a unit of work published to the queue.
type Job struct {
	JobID    string          `json:"job_id"`
	Type     string          `json:"job_type"`
	EntityID string          `json:"entity_id"`
	Metadata json.RawMessage `json:"metadata"`
}

// Publisher sends Job messages to RabbitMQ.
// It reconnects lazily on each Publish call if the connection is not ready.
type Publisher struct {
	url  string
	conn *amqp.Connection
	ch   *amqp.Channel
}

// NewPublisher returns a Publisher for the given AMQP URL. It retries the
// connection with exponential backoff for up to 30 seconds to tolerate slow
// broker startup, then continues with lazy reconnect on each Publish call.
func NewPublisher(url string) *Publisher {
	p := &Publisher{url: url}
	b := backoff.NewExponentialBackOff()
	b.MaxElapsedTime = 30 * time.Second
	if err := backoff.Retry(p.connect, b); err != nil {
		log.Printf("queue: RabbitMQ unavailable after 30s — will retry on each publish")
	}
	return p
}

func (p *Publisher) connect() error {
	conn, err := amqp.Dial(p.url)
	if err != nil {
		return err
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return err
	}
	if _, err = ch.QueueDeclare(QueueName, true, false, false, false, nil); err != nil {
		conn.Close()
		return err
	}
	p.conn = conn
	p.ch = ch
	return nil
}

func (p *Publisher) ready() bool {
	return p.conn != nil && !p.conn.IsClosed() && p.ch != nil
}

// Publish serializes a Job and publishes it as a persistent message.
// If the connection is not ready it attempts one reconnect before returning an error.
func (p *Publisher) Publish(ctx context.Context, job Job) error {
	if !p.ready() {
		if err := p.connect(); err != nil {
			return err
		}
	}
	body, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return p.ch.PublishWithContext(ctx, "", QueueName, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	})
}
