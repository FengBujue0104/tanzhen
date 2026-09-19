package hub

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/FengBujue0104/tanzhen/internal/models"
)

func TestCreateHeartbeatAndStatus(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	id, token, err := store.CreateNode("测试节点", models.NodeMeta{
		TrafficRemain: "100GB",
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

	gotID, name, err := store.NodeByToken(token)
	if err != nil || gotID != id || name != "测试节点" {
		t.Fatalf("NodeByToken: %v %s %s", err, gotID, name)
	}

	hb := &models.Heartbeat{CPUUsage: 12.5, MemUsage: 40, Hostname: "box"}
	store.SaveHeartbeat(id, hb)

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

	// offline after timeout — simulate by rewinding seen
	store.mu.Lock()
	store.seen[id] = time.Now().Add(-time.Minute)
	store.mu.Unlock()
	list, _ = store.ListStatus()
	if list[0].Online {
		t.Fatal("expected offline")
	}
}
