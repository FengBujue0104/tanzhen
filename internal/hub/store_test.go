package hub

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/FengBujue0104/tanzhen/internal/models"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "t.db"), StoreOptions{
		OfflineAfter: 30 * time.Second,
		HistoryCap:   60,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestCreateHeartbeatAndStatus(t *testing.T) {
	store := newTestStore(t)

	id, token, err := store.CreateNode("测试节点", models.NodeMeta{
		TrafficQuota:  100 << 30,
		TrafficPeriod: 30,
		Bandwidth:     "500Mbps",
		RenewalDate:   "2026-12-01",
		Price:         "¥20/月",
		Location:      "HK",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || token == "" {
		t.Fatal("empty id/token")
	}

	gotID, name, meta, err := store.NodeByToken(token)
	if err != nil {
		t.Fatalf("NodeByToken: %v", err)
	}
	if gotID != id {
		t.Fatalf("NodeByToken id: %s != %s", gotID, id)
	}
	if name != "测试节点" {
		t.Fatalf("NodeByToken name: %s", name)
	}
	if meta.TrafficQuota != 100<<30 || meta.Price != "¥20/月" {
		t.Fatalf("NodeByToken meta: %+v", meta)
	}

	hb := &models.Heartbeat{
		CPUUsage: 12.5, MemUsage: 40, Hostname: "box",
		NetTotalUp: 1000, NetTotalDown: 2000,
	}
	store.SaveHeartbeat(id, hb, meta)

	list, err := store.ListStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].Online || list[0].Metrics == nil {
		t.Fatalf("bad status: %+v", list)
	}
	if list[0].Meta.Price != "¥20/月" {
		t.Fatalf("meta: %+v", list[0].Meta)
	}
	// The first heartbeat after creation anchors the baseline, so the quota
	// starts counting from now rather than from the host's boot.
	if got := list[0].Traffic; got.Unlimited || got.Used != 0 || got.Remaining < 0 {
		t.Fatalf("traffic after anchor: %+v", got)
	}
	if len(list[0].History) != 1 {
		t.Fatalf("history: %+v", list[0].History)
	}

	// Second heartbeat inside the period accrues delta.
	hb2 := &models.Heartbeat{CPUUsage: 20, NetTotalUp: 1500, NetTotalDown: 3000}
	store.SaveHeartbeat(id, hb2, meta)
	list, _ = store.ListStatus()
	tf := list[0].Traffic
	if tf.Used != 1500 || tf.UsedUp != 500 || tf.UsedDown != 1000 {
		t.Fatalf("traffic delta: %+v", tf)
	}
	if tf.Remaining != int64(100<<30)-1500 {
		t.Fatalf("remaining: %+v", tf)
	}

	// A counter that goes backwards is a host-side reset, not negative traffic.
	store.SaveHeartbeat(id, &models.Heartbeat{NetTotalUp: 10, NetTotalDown: 20}, meta)
	list, _ = store.ListStatus()
	if list[0].Traffic.UsedUp != 10 {
		t.Fatalf("counter reset: %+v", list[0].Traffic)
	}

	// offline after timeout — simulate by rewinding seen
	store.mu.Lock()
	store.seen[id] = time.Now().Add(-time.Minute)
	store.mu.Unlock()
	list, _ = store.ListStatus()
	if list[0].Online {
		t.Fatal("expected offline")
	}
}

// TestUnlimitedTrafficWithoutPeriod pins the "0 = 不统计" semantics: no period
// means the hub never rolls a baseline and reports unlimited.
func TestUnlimitedTrafficWithoutPeriod(t *testing.T) {
	store := newTestStore(t)
	id, _, err := store.CreateNode("n", models.NodeMeta{TrafficQuota: 500 << 30})
	if err != nil {
		t.Fatal(err)
	}
	store.SaveHeartbeat(id, &models.Heartbeat{NetTotalUp: 999, NetTotalDown: 999}, models.NodeMeta{})
	list, _ := store.ListStatus()
	tf := list[0].Traffic
	if !tf.Unlimited || tf.Remaining != -1 || tf.Pct != -1 {
		t.Fatalf("expected unlimited: %+v", tf)
	}
}

func TestDeleteNode(t *testing.T) {
	store := newTestStore(t)
	id, _, err := store.CreateNode("n", models.NodeMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteNode(id); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteNode(id); err == nil {
		t.Fatal("expected ErrNoRows on the second delete")
	}
	n, _ := store.NodeCount()
	if n != 0 {
		t.Fatalf("count: %d", n)
	}
}

func TestAdvanceReset(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)

	// zero prev starts a fresh period from now
	if got := advanceReset(time.Time{}, 30, now); !got.Equal(now.AddDate(0, 0, 30)) {
		t.Fatalf("fresh period: %s", got)
	}
	// a stale prev is walked forward to the first boundary after now
	stale := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got := advanceReset(stale, 30, now)
	if !got.After(now) {
		t.Fatalf("not after now: %s", got)
	}
	if got.Sub(stale)%(30*24*time.Hour) != 0 {
		t.Fatalf("not on a 30-day boundary: %s", got)
	}
	// period <= 0 disables rolling entirely
	if !advanceReset(stale, 0, now).IsZero() {
		t.Fatal("period 0 must be zero")
	}
}

func TestHistoryRing(t *testing.T) {
	s := newTestStore(t)
	s.historyCap = 3
	id, _, _ := s.CreateNode("n", models.NodeMeta{})
	for i := 0; i < 10; i++ {
		s.pushHistory(id, models.Sample{T: int64(i), CPU: float64(i)})
	}
	h := s.history[id]
	if len(h) != 3 {
		t.Fatalf("cap: %d", len(h))
	}
	if h[0].T != 7 || h[2].T != 9 {
		t.Fatalf("ring contents: %+v", h)
	}
}
