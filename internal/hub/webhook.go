package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

const webhookTimeout = 5 * time.Second

// alertState is the in-memory debounce for optional webhook notifications.
// Empty WEBHOOK_URL means checkAlerts is a no-op.
type alertState struct {
	mu             sync.Mutex
	wasOnline      map[string]bool
	offlineAlerted map[string]time.Time
	trafficAlerted map[string]struct{}
	errLogged      bool
	client         *http.Client
}

func newAlertState() *alertState {
	return &alertState{
		wasOnline:      make(map[string]bool),
		offlineAlerted: make(map[string]time.Time),
		trafficAlerted: make(map[string]struct{}),
		client:         &http.Client{Timeout: webhookTimeout},
	}
}

// checkAlerts looks at current node status and POSTs JSON to WEBHOOK_URL on
// online→offline transitions and on traffic quota crossing WEBHOOK_TRAFFIC_PCT.
// Errors are ignored after a single log line.
func (s *Server) checkAlerts() {
	if s == nil || s.cfg.WebhookURL == "" || s.Store == nil || s.alerts == nil {
		return
	}
	list, err := s.Store.ListStatus()
	if err != nil {
		return
	}
	now := time.Now()
	cooldown := s.Store.OfflineAfter() * 2
	if cooldown <= 0 {
		cooldown = DefaultOfflineAfter * 2
	}
	thresh := s.cfg.WebhookTrafficPct
	if thresh <= 0 {
		thresh = 90
	}

	var toSend []map[string]any
	live := make(map[string]struct{}, len(list))

	s.alerts.mu.Lock()
	for _, n := range list {
		live[n.ID] = struct{}{}
		prev := s.alerts.wasOnline[n.ID]
		if prev && !n.Online {
			last, fired := s.alerts.offlineAlerted[n.ID]
			if !fired || now.Sub(last) >= cooldown {
				s.alerts.offlineAlerted[n.ID] = now
				payload := map[string]any{
					"event":   "node.offline",
					"node_id": n.ID,
					"name":    n.Name,
					"ts":      now.Unix(),
				}
				if !n.LastSeen.IsZero() {
					payload["last_seen"] = n.LastSeen.UTC().Format(time.RFC3339Nano)
				}
				toSend = append(toSend, payload)
			}
		}
		if n.Online {
			delete(s.alerts.offlineAlerted, n.ID)
		}
		s.alerts.wasOnline[n.ID] = n.Online

		tf := n.Traffic
		over := tf.Quota > 0 && !tf.Unlimited && tf.Pct >= thresh
		if over {
			if _, already := s.alerts.trafficAlerted[n.ID]; !already {
				s.alerts.trafficAlerted[n.ID] = struct{}{}
				toSend = append(toSend, map[string]any{
					"event":   "node.traffic",
					"node_id": n.ID,
					"name":    n.Name,
					"pct":     tf.Pct,
					"used":    tf.Used,
					"quota":   tf.Quota,
				})
			}
		} else {
			delete(s.alerts.trafficAlerted, n.ID)
		}
	}
	for id := range s.alerts.wasOnline {
		if _, ok := live[id]; !ok {
			delete(s.alerts.wasOnline, id)
			delete(s.alerts.offlineAlerted, id)
			delete(s.alerts.trafficAlerted, id)
		}
	}
	s.alerts.mu.Unlock()

	for _, p := range toSend {
		s.postWebhook(p)
	}
}

func (s *Server) postWebhook(payload any) {
	if s == nil || s.alerts == nil {
		return
	}
	url := s.cfg.WebhookURL
	if url == "" {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	client := s.alerts.client
	if client == nil {
		client = &http.Client{Timeout: webhookTimeout}
	}
	ctx, cancel := context.WithTimeout(context.Background(), webhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		s.logWebhookErr(err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		s.logWebhookErr(err)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		s.logWebhookErr(fmt.Errorf("status %d", resp.StatusCode))
		return
	}
	s.alerts.mu.Lock()
	s.alerts.errLogged = false
	s.alerts.mu.Unlock()
}

func (s *Server) logWebhookErr(err error) {
	if s.alerts == nil {
		return
	}
	s.alerts.mu.Lock()
	defer s.alerts.mu.Unlock()
	if s.alerts.errLogged {
		return
	}
	s.alerts.errLogged = true
	log.Printf("hub: webhook: %v", err)
}
