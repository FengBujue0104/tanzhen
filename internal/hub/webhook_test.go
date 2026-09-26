package hub

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/FengBujue0104/tanzhen/internal/models"
)

type webhookInbox struct {
	mu    sync.Mutex
	posts []map[string]any
}

func (b *webhookInbox) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		b.mu.Lock()
		b.posts = append(b.posts, m)
		b.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}
}

func (b *webhookInbox) events() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.posts))
	for _, p := range b.posts {
		if e, _ := p["event"].(string); e != "" {
			out = append(out, e)
		}
	}
	return out
}

func (b *webhookInbox) last(event string) map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.posts) - 1; i >= 0; i-- {
		if e, _ := b.posts[i]["event"].(string); e == event {
			return b.posts[i]
		}
	}
	return nil
}

func TestWebhookEmptyURLNoop(t *testing.T) {
	var box webhookInbox
	ts := httptest.NewServer(box.handler())
	t.Cleanup(ts.Close)

	srv := newTestServer(t, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("x")}})
	id, _, err := srv.Store.CreateNode("n", models.NodeMeta{})
	if err != nil {
		t.Fatal(err)
	}
	srv.Store.SaveHeartbeat(id, &models.Heartbeat{CPUUsage: 1}, models.NodeMeta{})
	srv.Store.mu.Lock()
	srv.Store.seen[id] = time.Now().Add(-time.Minute)
	srv.Store.mu.Unlock()
	srv.checkAlerts()
	if n := len(box.events()); n != 0 {
		t.Fatalf("posted with empty WEBHOOK_URL: %v", box.events())
	}
}

func TestWebhookOfflineAndTraffic(t *testing.T) {
	var box webhookInbox
	ts := httptest.NewServer(box.handler())
	t.Cleanup(ts.Close)

	srv := newTestServer(t, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("x")}})
	srv.cfg.WebhookURL = ts.URL
	srv.cfg.WebhookTrafficPct = 90

	meta := models.NodeMeta{TrafficQuota: 1000, TrafficPeriod: 30}
	id, _, err := srv.Store.CreateNode("box", meta)
	if err != nil {
		t.Fatal(err)
	}

	srv.Store.SaveHeartbeat(id, &models.Heartbeat{NetTotalUp: 0, NetTotalDown: 0}, meta)
	srv.checkAlerts()
	if n := len(box.events()); n != 0 {
		t.Fatalf("unexpected posts while online/low: %v", box.events())
	}

	srv.Store.mu.Lock()
	srv.Store.seen[id] = time.Now().Add(-time.Minute)
	srv.Store.mu.Unlock()
	srv.checkAlerts()
	if ev := box.events(); len(ev) != 1 || ev[0] != "node.offline" {
		t.Fatalf("offline transition: %v", ev)
	}
	got := box.last("node.offline")
	if got["node_id"] != id || got["name"] != "box" {
		t.Fatalf("offline payload: %+v", got)
	}
	if _, ok := got["last_seen"]; !ok {
		t.Fatalf("missing last_seen: %+v", got)
	}

	srv.checkAlerts()
	if ev := box.events(); len(ev) != 1 {
		t.Fatalf("offline re-fired: %v", ev)
	}

	srv.Store.SaveHeartbeat(id, &models.Heartbeat{NetTotalUp: 900, NetTotalDown: 0}, meta)
	srv.checkAlerts()
	if ev := box.events(); len(ev) != 2 || ev[1] != "node.traffic" {
		t.Fatalf("traffic fire: %v", ev)
	}
	tf := box.last("node.traffic")
	if tf["node_id"] != id || tf["quota"] != float64(1000) {
		t.Fatalf("traffic payload: %+v", tf)
	}
	pct, _ := tf["pct"].(float64)
	if pct < 90 {
		t.Fatalf("pct: %v", tf["pct"])
	}

	srv.checkAlerts()
	if ev := box.events(); len(ev) != 2 {
		t.Fatalf("traffic re-fired: %v", ev)
	}

	// Drop below the threshold, then climb again — should fire once more.
	srv.Store.SaveHeartbeat(id, &models.Heartbeat{NetTotalUp: 100, NetTotalDown: 0}, meta)
	srv.checkAlerts()
	srv.Store.SaveHeartbeat(id, &models.Heartbeat{NetTotalUp: 950, NetTotalDown: 0}, meta)
	srv.checkAlerts()
	if ev := box.events(); len(ev) != 3 || ev[2] != "node.traffic" {
		t.Fatalf("traffic after drop: %v", ev)
	}

	// Back online then offline again should re-fire node.offline.
	srv.Store.SaveHeartbeat(id, &models.Heartbeat{NetTotalUp: 950, NetTotalDown: 0}, meta)
	srv.checkAlerts()
	srv.Store.mu.Lock()
	srv.Store.seen[id] = time.Now().Add(-time.Minute)
	srv.Store.mu.Unlock()
	srv.checkAlerts()
	ev := box.events()
	if len(ev) < 4 || ev[len(ev)-1] != "node.offline" {
		t.Fatalf("second offline: %v", ev)
	}
}
