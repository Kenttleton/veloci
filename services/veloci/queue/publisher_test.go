package queue_test

import (
	"encoding/json"
	"testing"

	"github.com/veloci/api/queue"
)

func TestJobSerializesCorrectly(t *testing.T) {
	job := queue.Job{
		JobID:    "job-123",
		Type:     "import.process",
		EntityID: "ent-1",
		Metadata: json.RawMessage(`{"pending_import_id":"imp-1"}`),
	}
	body, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["job_id"] != "job-123" {
		t.Errorf("job_id: got %v", m["job_id"])
	}
	if m["type"] != "import.process" {
		t.Errorf("type: got %v", m["type"])
	}
	if m["entity_id"] != "ent-1" {
		t.Errorf("entity_id: got %v", m["entity_id"])
	}
	meta, ok := m["metadata"].(map[string]any)
	if !ok {
		t.Fatal("metadata is not an object")
	}
	if meta["pending_import_id"] != "imp-1" {
		t.Errorf("metadata.pending_import_id: got %v", meta["pending_import_id"])
	}
}

func TestJobRoundTrips(t *testing.T) {
	original := queue.Job{
		JobID:    "j",
		Type:     "rules.reprocess",
		EntityID: "e",
		Metadata: json.RawMessage(`{}`),
	}
	body, _ := json.Marshal(original)

	var decoded queue.Job
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.JobID != original.JobID {
		t.Errorf("job_id: got %q want %q", decoded.JobID, original.JobID)
	}
	if decoded.Type != original.Type {
		t.Errorf("type: got %q want %q", decoded.Type, original.Type)
	}
	if decoded.EntityID != original.EntityID {
		t.Errorf("entity_id: got %q want %q", decoded.EntityID, original.EntityID)
	}
}

func TestNewPublisher_StartsWithUnreachableHost(t *testing.T) {
	// The publisher must not fail at construction time — the API should start
	// even when RabbitMQ is unavailable and reconnect on the first publish.
	pub := queue.NewPublisher("amqp://localhost:1/")
	if pub == nil {
		t.Fatal("expected non-nil publisher even when RabbitMQ is unreachable")
	}
}
