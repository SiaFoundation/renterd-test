package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
)

type Broadcaster interface {
	BroadcastAction(ctx context.Context, action Event) error
}

const (
	webhookTimeout   = 10 * time.Second
	WebhookEventPing = "ping"
)

type (
	Webhook struct {
		Module string `json:"module"`
		Event  string `json:"event"`
		URL    string `json:"url"`
	}

	WebhookQueueInfo struct {
		URL  string `json:"url"`
		Size int    `json:"size"`
	}

	// Event describes an event that has been triggered.
	Event struct {
		Module  string      `json:"module"`
		Event   string      `json:"event"`
		Payload interface{} `json:"payload,omitempty"`
	}
)

type Manager struct {
	ctx       context.Context
	ctxCancel context.CancelFunc
	logger    *zap.SugaredLogger
	wg        sync.WaitGroup

	mu       sync.Mutex
	queues   map[string]*eventQueue // URL -> queue
	webhooks map[string]Webhook
}

type eventQueue struct {
	ctx    context.Context
	logger *zap.SugaredLogger
	url    string

	mu           sync.Mutex
	isDequeueing bool
	events       []Event
}

func (w *Manager) Close() error {
	w.ctxCancel()
	w.wg.Wait()
	return nil
}

func (w Webhook) String() string {
	return fmt.Sprintf("%v.%v.%v", w.URL, w.Module, w.Event)
}

func (w *Manager) Register(wh Webhook) error {
	ctx, cancel := context.WithTimeout(context.Background(), webhookTimeout)
	defer cancel()

	// Test URL.
	err := sendEvent(ctx, wh.URL, Event{
		Event: WebhookEventPing,
	})
	if err != nil {
		return err
	}

	// Add Webhook.
	w.mu.Lock()
	defer w.mu.Unlock()
	w.webhooks[wh.String()] = wh
	return nil
}

func (w *Manager) Delete(wh Webhook) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, exists := w.webhooks[wh.String()]
	delete(w.webhooks, wh.String())
	return exists
}

func (w *Manager) Info() ([]Webhook, []WebhookQueueInfo) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var hooks []Webhook
	for _, hook := range w.webhooks {
		hooks = append(hooks, Webhook{
			Event:  hook.Event,
			Module: hook.Module,
			URL:    hook.URL,
		})
	}
	var queueInfos []WebhookQueueInfo
	for _, queue := range w.queues {
		queue.mu.Lock()
		queueInfos = append(queueInfos, WebhookQueueInfo{
			URL:  queue.url,
			Size: len(queue.events),
		})
		queue.mu.Unlock()
	}
	return hooks, queueInfos
}

func (a Event) String() string {
	return a.Module + "." + a.Event
}

func (w *Manager) BroadcastAction(_ context.Context, event Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, hook := range w.webhooks {
		if !hook.Matches(event) {
			continue
		}

		// Find queue or create one.
		queue, exists := w.queues[hook.URL]
		if !exists {
			queue = &eventQueue{
				ctx:    w.ctx,
				logger: w.logger,
				url:    hook.URL,
			}
			w.queues[hook.URL] = queue
		}

		// Add event and launch goroutine to start dequeueing if necessary.
		queue.mu.Lock()
		queue.events = append(queue.events, event)
		if !queue.isDequeueing {
			queue.isDequeueing = true
			w.wg.Add(1)
			go func() {
				queue.dequeue()
				w.wg.Done()
			}()
		}
		queue.mu.Unlock()
	}
	return nil
}

func (q *eventQueue) dequeue() {
	for {
		q.mu.Lock()
		if len(q.events) == 0 {
			q.isDequeueing = false
			q.mu.Unlock()
			return
		}
		next := q.events[0]
		q.events = q.events[1:]
		q.mu.Unlock()

		err := sendEvent(q.ctx, q.url, next)
		if err != nil {
			q.logger.Errorf("failed to send Webhook event %v to %v: %v", next.String(), q.url, err)
			return
		}
	}
}

func (w Webhook) Matches(action Event) bool {
	if w.Module != action.Module {
		return false
	}
	return w.Event == "" || w.Event == action.Event
}

func NewManager(logger *zap.SugaredLogger) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		ctx:       ctx,
		ctxCancel: cancel,
		logger:    logger.Named("webhooks"),
		queues:    make(map[string]*eventQueue),
		webhooks:  make(map[string]Webhook),
	}
}

func sendEvent(ctx context.Context, url string, action Event) error {
	body, err := json.Marshal(action)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer io.ReadAll(req.Body) // always drain body

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		errStr, err := io.ReadAll(req.Body)
		if err != nil {
			return fmt.Errorf("failed to read response body: %w", err)
		}
		return fmt.Errorf("Webhook returned unexpected status %v: %v", resp.StatusCode, string(errStr))
	}
	return nil
}