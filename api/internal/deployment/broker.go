package deployment

import "sync"

// Event is one message pushed to browsers watching a deployment.
type Event struct {
	Type   string `json:"type"`             // "log" or "status"
	Line   int    `json:"line,omitempty"`   // log: line number
	Text   string `json:"text,omitempty"`   // log: the line
	Status Status `json:"status,omitempty"` // status: the new status
	Error  string `json:"error,omitempty"`  // status failed: why
}

// Broker is an in-memory publish/subscribe hub: the worker PUBLISHES events
// for a deployment, and every open browser tab watching it SUBSCRIBES.
//
// Each subscriber gets its own buffered channel. The worker never waits for a
// slow browser: if a subscriber's buffer is full, the broker drops that
// subscriber (closes its channel). Its browser reconnects automatically and
// replays the missed lines from the database, so nothing is lost.
//
// Limitation: it lives in one process's memory, so it only works with ONE API
// server. Several servers would need a shared broker (Redis pub/sub, NATS).
type Broker struct {
	mu   sync.Mutex
	subs map[int64]map[chan Event]struct{} // deployment id -> set of subscriber channels
}

func NewBroker() *Broker {
	return &Broker{subs: map[int64]map[chan Event]struct{}{}}
}

const subscriberBuffer = 256

// Subscribe returns a channel of events for one deployment and a function
// that unsubscribes (always call it, typically with defer).
func (b *Broker) Subscribe(deploymentID int64) (<-chan Event, func()) {
	ch := make(chan Event, subscriberBuffer)

	b.mu.Lock()
	if b.subs[deploymentID] == nil {
		b.subs[deploymentID] = map[chan Event]struct{}{}
	}
	b.subs[deploymentID][ch] = struct{}{}
	b.mu.Unlock()

	unsubscribe := func() { b.remove(deploymentID, ch) }
	return ch, unsubscribe
}

// Publish sends e to every subscriber of the deployment without blocking.
func (b *Broker) Publish(deploymentID int64, e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for ch := range b.subs[deploymentID] {
		select {
		case ch <- e: // delivered
		default: // buffer full: drop this slow subscriber
			b.removeLocked(deploymentID, ch)
		}
	}
}

func (b *Broker) remove(deploymentID int64, ch chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.removeLocked(deploymentID, ch)
}

// removeLocked must be called with b.mu held. It is safe to call twice for
// the same channel: the map lookup makes sure the channel is closed once.
func (b *Broker) removeLocked(deploymentID int64, ch chan Event) {
	set := b.subs[deploymentID]
	if _, ok := set[ch]; !ok {
		return
	}
	delete(set, ch)
	close(ch)
	if len(set) == 0 {
		delete(b.subs, deploymentID)
	}
}
