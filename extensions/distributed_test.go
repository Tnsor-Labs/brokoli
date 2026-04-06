package extensions

import (
	"sync"
	"testing"
	"time"
)

func TestInMemoryEventBus_PublishSubscribe(t *testing.T) {
	bus := newInMemoryEventBus()

	msgs, closer, err := bus.Subscribe("events:run")
	if err != nil {
		t.Fatal(err)
	}
	defer closer()

	payload := []byte(`{"type":"run.started"}`)
	if err := bus.Publish("events:run", payload); err != nil {
		t.Fatal(err)
	}

	select {
	case msg := <-msgs:
		if msg.Channel != "events:run" {
			t.Errorf("expected channel events:run, got %s", msg.Channel)
		}
		if string(msg.Data) != string(payload) {
			t.Errorf("expected payload %s, got %s", payload, msg.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for message")
	}
}

func TestInMemoryEventBus_Pattern(t *testing.T) {
	bus := newInMemoryEventBus()

	msgs, closer, err := bus.Subscribe("events:*")
	if err != nil {
		t.Fatal(err)
	}
	defer closer()

	bus.Publish("events:run", []byte("run"))
	bus.Publish("events:org:123", []byte("org"))
	bus.Publish("other:stuff", []byte("nope"))

	received := 0
	timeout := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case <-msgs:
			received++
		case <-timeout:
			break loop
		}
	}

	if received != 2 {
		t.Errorf("expected 2 messages matching events:*, got %d", received)
	}
}

func TestInMemoryEventBus_ExactMatch(t *testing.T) {
	bus := newInMemoryEventBus()

	msgs, closer, err := bus.Subscribe("events:run")
	if err != nil {
		t.Fatal(err)
	}
	defer closer()

	bus.Publish("events:run:extra", []byte("no"))
	bus.Publish("events:run", []byte("yes"))

	select {
	case msg := <-msgs:
		if string(msg.Data) != "yes" {
			t.Errorf("expected exact match only, got %s", msg.Data)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out")
	}
}

func TestInMemoryEventBus_MultipleSubscribers(t *testing.T) {
	bus := newInMemoryEventBus()

	msgs1, closer1, _ := bus.Subscribe("events:*")
	defer closer1()
	msgs2, closer2, _ := bus.Subscribe("events:*")
	defer closer2()

	bus.Publish("events:test", []byte("hello"))

	for _, ch := range []<-chan EventMessage{msgs1, msgs2} {
		select {
		case msg := <-ch:
			if string(msg.Data) != "hello" {
				t.Errorf("unexpected data: %s", msg.Data)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out")
		}
	}
}

func TestInMemoryJobQueue_EnqueueDequeue(t *testing.T) {
	q := newInMemoryJobQueue()

	job := RunJob{
		ID:         "j1",
		PipelineID: "pipe1",
		RunID:      "run1",
		OrgID:      "org1",
		Priority:   0,
		EnqueuedAt: time.Now(),
	}

	if err := q.Enqueue(job); err != nil {
		t.Fatal(err)
	}

	if q.Len() != 1 {
		t.Errorf("expected len 1, got %d", q.Len())
	}

	got, err := q.Dequeue()
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "j1" {
		t.Errorf("expected job id j1, got %s", got.ID)
	}
	if got.PipelineID != "pipe1" {
		t.Errorf("expected pipeline_id pipe1, got %s", got.PipelineID)
	}
}

func TestInMemoryJobQueue_FIFO(t *testing.T) {
	q := newInMemoryJobQueue()

	for i := 0; i < 3; i++ {
		q.Enqueue(RunJob{ID: string(rune('a' + i))})
	}

	for i := 0; i < 3; i++ {
		got, _ := q.Dequeue()
		expected := string(rune('a' + i))
		if got.ID != expected {
			t.Errorf("expected %s, got %s", expected, got.ID)
		}
	}
}

func TestInMemoryJobQueue_Close(t *testing.T) {
	q := newInMemoryJobQueue()

	// Enqueue a job, close, then dequeue should get it
	q.Enqueue(RunJob{ID: "last"})
	q.Close()

	// Enqueue after close should fail
	err := q.Enqueue(RunJob{ID: "nope"})
	if err != ErrQueueClosed {
		t.Errorf("expected ErrQueueClosed, got %v", err)
	}

	// Dequeue should get buffered job then ErrQueueClosed
	got, err := q.Dequeue()
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "last" {
		t.Errorf("expected last, got %s", got.ID)
	}

	_, err = q.Dequeue()
	if err != ErrQueueClosed {
		t.Errorf("expected ErrQueueClosed, got %v", err)
	}
}

func TestInMemoryJobQueue_Concurrent(t *testing.T) {
	q := newInMemoryJobQueue()
	n := 100

	// Concurrent enqueue
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q.Enqueue(RunJob{ID: string(rune(i))})
		}(i)
	}
	wg.Wait()

	if q.Len() != n {
		t.Errorf("expected %d jobs, got %d", n, q.Len())
	}

	// Concurrent dequeue
	received := int32(0)
	var mu sync.Mutex
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := q.Dequeue()
			if err == nil {
				mu.Lock()
				received++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if received != int32(n) {
		t.Errorf("expected %d dequeued, got %d", n, received)
	}
}

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		pattern, channel string
		want             bool
	}{
		{"events:run", "events:run", true},
		{"events:*", "events:run", true},
		{"events:*", "events:org:123", true},
		{"*", "anything", true},
		{"events:run", "events:run:extra", false},
		{"events:*", "other:stuff", false},
		{"exact", "other", false},
	}
	for _, tt := range tests {
		got := matchPattern(tt.pattern, tt.channel)
		if got != tt.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.pattern, tt.channel, got, tt.want)
		}
	}
}
