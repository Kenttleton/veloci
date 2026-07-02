// Package queue provides RabbitMQ publishing for the veloci-api service.
package queue

import (
	"context"
	"encoding/json"

	amqp "github.com/rabbitmq/amqp091-go"
)

// QueueName is the durable RabbitMQ queue used for veloci job messages.
const QueueName = "veloci.jobs"

// Job represents a unit of work published to the queue.
type Job struct {
	JobID    string          `json:"job_id"`
	Type     string          `json:"type"`
	EntityID string          `json:"entity_id"`
	Metadata json.RawMessage `json:"metadata"`
}

// Publisher sends Job messages to RabbitMQ.
type Publisher struct {
	ch    *amqp.Channel
	queue string
}

// NewPublisher dials RabbitMQ, opens a channel, and declares the durable queue.
func NewPublisher(url string) (*Publisher, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		return nil, err
	}
	_, err = ch.QueueDeclare(QueueName, true, false, false, false, nil)
	if err != nil {
		return nil, err
	}
	return &Publisher{ch: ch, queue: QueueName}, nil
}

// Publish serializes a Job and publishes it as a persistent message.
func (p *Publisher) Publish(ctx context.Context, job Job) error {
	body, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return p.ch.PublishWithContext(ctx, "", p.queue, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	})
}
