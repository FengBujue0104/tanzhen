package hub

import (
	"bytes"
	"encoding/json"
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

func countSamples(t *testing.T, s *Store, id string) int {
	t.Helper()
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM samples WHERE node_id = ?`, id).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestHistoryDownsample(t *testing.T) {
	store := newTestStore(t)
	store.persistEvery = 15 * time.Second
	id, _, err := store.CreateNode("n", models.NodeMeta{})
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return t0 }
	store.SaveHeartbeat(id, &models.Heartbeat{CPUUsage: 1}, models.NodeMeta{})
	store.now = func() time.Time { return t0.Add(5 * time.Second) }
	store.SaveHeartbeat(id, &models.Heartbeat{CPUUsage: 2}, models.NodeMeta{})
	store.now = func() time.Time { return t0.Add(14 * time.Second) }
	store.SaveHeartbeat(id, &models.Heartbeat{CPUUsage: 3}, models.NodeMeta{})
	if n := countSamples(t, store, id); n != 1 {
		t.Fatalf("downsample before 15s: %d", n)
	}
	store.now = func() time.Time { return t0.Add(15 * time.Second) }
	store.SaveHeartbeat(id, &models.Heartbeat{CPUUsage: 4}, models.NodeMeta{})
	if n := countSamples(t, store, id); n != 2 {
		t.Fatalf("persist at 15s: %d", n)
	}
	if len(store.history[id]) != 4 {
		t.Fatalf("memory ring: %d", len(store.history[id]))
	}
	got, err := store.ListHistory(id, t0.Unix(), t0.Add(15*time.Second).Unix())
	if err != nil || len(got) != 2 || got[0].CPU != 1 || got[1].CPU != 4 {
		t.Fatalf("persisted: %+v %v", got, err)
	}
}

func TestHistoryRetention(t *testing.T) {
	store := newTestStore(t)
	store.retention = 2 * time.Hour
	id, _, err := store.CreateNode("n", models.NodeMeta{})
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return t0 }
	old := models.Sample{T: t0.Add(-3 * time.Hour).Unix(), CPU: 1, LatCT: -1, LatCU: -1, LatCM: -1, LossCT: -1, LossCU: -1, LossCM: -1}
	keep := models.Sample{T: t0.Add(-30 * time.Minute).Unix(), CPU: 2, LatCT: -1, LatCU: -1, LatCM: -1, LossCT: -1, LossCU: -1, LossCM: -1}
	store.insertSample(id, old)
	store.insertSample(id, keep)
	store.PruneSamples()
	if n := countSamples(t, store, id); n != 1 {
		t.Fatalf("retention: %d", n)
	}
	got, err := store.ListHistory(id, 0, t0.Unix())
	if err != nil || len(got) != 1 || got[0].CPU != 2 {
		t.Fatalf("kept: %+v %v", got, err)
	}
}

func TestHistoryHydrateOnReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	opts := StoreOptions{HistoryCap: 4, PersistEvery: time.Second, Retention: 2 * time.Hour}
	s1, err := NewStore(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := s1.CreateNode("n", models.NodeMeta{})
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Unix(1_700_000_000, 0)
	for i := 0; i < 6; i++ {
		s1.insertSample(id, models.Sample{
			T:   t0.Add(time.Duration(i) * time.Minute).Unix(),
			CPU: float64(i), LatCT: 10 + float64(i), LatCU: -1, LatCM: -1,
			LossCT: 0, LossCU: -1, LossCM: -1,
		})
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := NewStore(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	h := s2.history[id]
	if len(h) != 4 {
		t.Fatalf("hydrated cap: %d %+v", len(h), h)
	}
	if h[0].T != t0.Add(2*time.Minute).Unix() || h[3].CPU != 5 {
		t.Fatalf("hydrated contents: %+v", h)
	}
	if h[0].LatCT != 12 {
		t.Fatalf("hydrated lat: %+v", h[0])
	}
	if s2.lastPersist[id] != h[3].T {
		t.Fatalf("lastPersist: %d", s2.lastPersist[id])
	}
	list, err := s2.ListStatus()
	if err != nil || len(list) != 1 || len(list[0].History) != 4 {
		t.Fatalf("status history after reopen: %+v %v", list, err)
	}
}

func TestDeleteNodeClearsSamples(t *testing.T) {
	store := newTestStore(t)
	id, _, err := store.CreateNode("n", models.NodeMeta{})
	if err != nil {
		t.Fatal(err)
	}
	store.SaveHeartbeat(id, &models.Heartbeat{CPUUsage: 1}, models.NodeMeta{})
	if countSamples(t, store, id) != 1 {
		t.Fatal("expected one sample")
	}
	if err := store.DeleteNode(id); err != nil {
		t.Fatal(err)
	}
	if n := countSamples(t, store, id); n != 0 {
		t.Fatalf("samples after delete: %d", n)
	}
	if _, ok := store.history[id]; ok {
		t.Fatal("memory history leftover")
	}
	if _, ok := store.lastPersist[id]; ok {
		t.Fatal("lastPersist leftover")
	}
}

func TestSampleLatencyFields(t *testing.T) {
	store := newTestStore(t)
	id, _, err := store.CreateNode("n", models.NodeMeta{})
	if err != nil {
		t.Fatal(err)
	}
	hb := &models.Heartbeat{
		CPUUsage: 1, MemUsage: 2,
		LatencyCT: 12.5, LatencyCU: -1, LatencyCM: 40,
		LossCT: 0.5, LossCU: -1, LossCM: 1.25,
	}
	store.SaveHeartbeat(id, hb, models.NodeMeta{})
	list, err := store.ListStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || len(list[0].History) != 1 {
		t.Fatalf("status: %+v", list)
	}
	sm := list[0].History[0]
	if sm.LatCT != 12.5 || sm.LatCU != -1 || sm.LatCM != 40 ||
		sm.LossCT != 0.5 || sm.LossCU != -1 || sm.LossCM != 1.25 {
		t.Fatalf("latency fields: %+v", sm)
	}
	b, err := json.Marshal(sm)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"lat_ct":12.5`, `"lat_cu":-1`, `"lat_cm":40`,
		`"loss_ct":0.5`, `"loss_cu":-1`, `"loss_cm":1.25`,
	} {
		if !bytes.Contains(b, []byte(want)) {
			t.Fatalf("json missing %s in %s", want, b)
		}
	}
	got, err := store.ListHistory(id, sm.T, sm.T)
	if err != nil || len(got) != 1 || got[0].LatCT != 12.5 || got[0].LatCU != -1 {
		t.Fatalf("sqlite latency: %+v %v", got, err)
	}
}
