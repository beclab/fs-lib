package app

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"bytetrade.io/web3os/fs-lib/jfsnotify"
)

const testDebounce = 30 * time.Millisecond

type sendCapture struct {
	mu     sync.Mutex
	events []jfsnotify.Event
}

func (c *sendCapture) hook() func(*jfsnotify.Event) error {
	return func(event *jfsnotify.Event) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.events = append(c.events, *event)
		return nil
	}
}

func (c *sendCapture) snapshot() []jfsnotify.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]jfsnotify.Event, len(c.events))
	copy(out, c.events)
	return out
}

func (c *sendCapture) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func eventJSON(t *testing.T, name, key string, op jfsnotify.Op) string {
	t.Helper()
	b, err := json.Marshal([]*jfsnotify.Event{{Name: name, Key: key, Op: op}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWriteDebounce_CoalesceSamePath(t *testing.T) {
	old := writeDebounce
	writeDebounce = testDebounce
	defer func() { writeDebounce = old }()

	cap := &sendCapture{}
	w := NewWatcher(nil)
	w.testSendEvent = cap.hook()

	key := "/node/docs"
	name := "/node/docs/ok7.txt"
	for i := 0; i < 3; i++ {
		// Distinct Key suffix would change Name in real life; keep same Name, vary nothing
		// except we still send 3 WRITE payloads for the same path.
		if err := w.WriteMsg(eventJSON(t, name, key, jfsnotify.Write)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	waitUntil(t, 300*time.Millisecond, func() bool { return cap.len() >= 1 })
	time.Sleep(2 * testDebounce) // ensure no second flush

	got := cap.snapshot()
	if len(got) != 1 {
		t.Fatalf("want 1 send after burst, got %d: %+v", len(got), got)
	}
	if got[0].Name != name || got[0].Key != key || got[0].Op != jfsnotify.Write {
		t.Fatalf("unexpected event: %+v", got[0])
	}
	w.mu.RLock()
	pending := len(w.delayedWrites)
	w.mu.RUnlock()
	if pending != 0 {
		t.Fatalf("delayedWrites should be empty after flush, got %d", pending)
	}
}

func TestWriteDebounce_FlushLatestSnapshot(t *testing.T) {
	old := writeDebounce
	writeDebounce = testDebounce
	defer func() { writeDebounce = old }()

	cap := &sendCapture{}
	w := NewWatcher(nil)
	send := cap.hook()
	name := "/node/docs/a.txt"
	w.armWriteDebounce(jfsnotify.Event{Name: name, Key: "/node/docs/v1", Op: jfsnotify.Write}, send)
	w.armWriteDebounce(jfsnotify.Event{Name: name, Key: "/node/docs/v2", Op: jfsnotify.Write}, send)

	waitUntil(t, 300*time.Millisecond, func() bool { return cap.len() == 1 })
	got := cap.snapshot()
	if got[0].Key != "/node/docs/v2" {
		t.Fatalf("want latest Key /node/docs/v2, got %+v", got[0])
	}
}

func TestWriteDebounce_SpacedWritesFlushTwice(t *testing.T) {
	old := writeDebounce
	writeDebounce = testDebounce
	defer func() { writeDebounce = old }()

	cap := &sendCapture{}
	w := NewWatcher(nil)
	w.testSendEvent = cap.hook()

	name := "/node/docs/ok7.txt"
	key := "/node/docs"
	if err := w.WriteMsg(eventJSON(t, name, key, jfsnotify.Write)); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 300*time.Millisecond, func() bool { return cap.len() == 1 })

	if err := w.WriteMsg(eventJSON(t, name, key, jfsnotify.Write)); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 300*time.Millisecond, func() bool { return cap.len() == 2 })

	if cap.len() != 2 {
		t.Fatalf("want 2 sends for spaced WRITEs, got %d", cap.len())
	}
}

func TestWriteDebounce_CloseCancelsPending(t *testing.T) {
	old := writeDebounce
	writeDebounce = 200 * time.Millisecond
	defer func() { writeDebounce = old }()

	cap := &sendCapture{}
	w := NewWatcher(nil)
	w.testSendEvent = cap.hook()
	removed := false
	w.Remove = func() { removed = true }

	if err := w.WriteMsg(eventJSON(t, "/node/docs/ok7.txt", "/node/docs", jfsnotify.Write)); err != nil {
		t.Fatal(err)
	}
	w.Close()

	time.Sleep(350 * time.Millisecond)
	if cap.len() != 0 {
		t.Fatalf("Close should cancel pending flush, got %d sends: %+v", cap.len(), cap.snapshot())
	}
	if !removed {
		t.Fatal("Close should call Remove")
	}
	w.mu.RLock()
	pending := len(w.delayedWrites)
	w.mu.RUnlock()
	if pending != 0 {
		t.Fatalf("delayedWrites should be cleared on Close, got %d", pending)
	}
}

func TestWriteMsg_ChmodImmediate(t *testing.T) {
	old := writeDebounce
	writeDebounce = testDebounce
	defer func() { writeDebounce = old }()

	cap := &sendCapture{}
	w := NewWatcher(nil)
	w.testSendEvent = cap.hook()

	name := "/node/docs/state"
	key := "/node/docs"
	if err := w.WriteMsg(eventJSON(t, name, key, jfsnotify.Chmod)); err != nil {
		t.Fatal(err)
	}

	if cap.len() != 1 {
		t.Fatalf("CHMOD should send immediately, got %d", cap.len())
	}
	w.mu.RLock()
	pending := len(w.delayedWrites)
	w.mu.RUnlock()
	if pending != 0 {
		t.Fatalf("CHMOD must not enter delayedWrites, got %d", pending)
	}

	time.Sleep(2 * testDebounce)
	if cap.len() != 1 {
		t.Fatalf("CHMOD should not trigger a later debounce flush, got %d", cap.len())
	}
}
