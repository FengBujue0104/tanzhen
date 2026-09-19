package hub

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/FengBujue0104/tanzhen/internal/models"
	_ "modernc.org/sqlite"
)

const offlineAfter = 30 * time.Second

type Store struct {
	db *sql.DB
	mu sync.RWMutex
	// in-memory latest metrics for fast reads
	metrics map[string]*models.Heartbeat
	seen    map[string]time.Time
}

func NewStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	s := &Store{
		db:      db,
		metrics: make(map[string]*models.Heartbeat),
		seen:    make(map[string]time.Time),
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS nodes (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  token TEXT NOT NULL UNIQUE,
  meta_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_nodes_token ON nodes(token);
`)
	return err
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Store) CreateNode(name string, meta models.NodeMeta) (id, token string, err error) {
	id = randHex(8)
	token = randHex(24)
	if name == "" {
		name = "节点-" + id[:6]
	}
	metaJSON, _ := json.Marshal(meta)
	_, err = s.db.Exec(
		`INSERT INTO nodes (id, name, token, meta_json, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, name, token, string(metaJSON), time.Now().UTC().Format(time.RFC3339),
	)
	return id, token, err
}

func (s *Store) DeleteNode(id string) error {
	res, err := s.db.Exec(`DELETE FROM nodes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("not found")
	}
	s.mu.Lock()
	delete(s.metrics, id)
	delete(s.seen, id)
	s.mu.Unlock()
	return nil
}

func (s *Store) UpdateNode(id string, name *string, meta *models.NodeMeta) error {
	row := s.db.QueryRow(`SELECT name, meta_json FROM nodes WHERE id = ?`, id)
	var curName, metaJSON string
	if err := row.Scan(&curName, &metaJSON); err != nil {
		return fmt.Errorf("not found")
	}
	if name != nil {
		curName = *name
	}
	if meta != nil {
		b, _ := json.Marshal(meta)
		metaJSON = string(b)
	}
	_, err := s.db.Exec(`UPDATE nodes SET name = ?, meta_json = ? WHERE id = ?`, curName, metaJSON, id)
	return err
}

func (s *Store) NodeByToken(token string) (id, name string, err error) {
	err = s.db.QueryRow(`SELECT id, name FROM nodes WHERE token = ?`, token).Scan(&id, &name)
	return
}

func (s *Store) SaveHeartbeat(id string, hb *models.Heartbeat) {
	s.mu.Lock()
	cp := *hb
	s.metrics[id] = &cp
	s.seen[id] = time.Now()
	s.mu.Unlock()
}

func (s *Store) ListStatus() ([]models.NodeStatus, error) {
	rows, err := s.db.Query(`SELECT id, name, meta_json, created_at FROM nodes ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []models.NodeStatus
	now := time.Now()
	for rows.Next() {
		var id, name, metaJSON, createdStr string
		if err := rows.Scan(&id, &name, &metaJSON, &createdStr); err != nil {
			return nil, err
		}
		var meta models.NodeMeta
		_ = json.Unmarshal([]byte(metaJSON), &meta)
		created, _ := time.Parse(time.RFC3339, createdStr)
		st := models.NodeStatus{
			ID:        id,
			Name:      name,
			CreatedAt: created,
			Meta:      meta,
		}
		if t, ok := s.seen[id]; ok {
			st.LastSeen = t
			st.Online = now.Sub(t) < offlineAfter
			if m, ok2 := s.metrics[id]; ok2 {
				cp := *m
				st.Metrics = &cp
			}
		}
		out = append(out, st)
	}
	if out == nil {
		out = []models.NodeStatus{}
	}
	return out, rows.Err()
}

func (s *Store) GetNodeAdmin(id string) (name, token string, meta models.NodeMeta, err error) {
	var metaJSON string
	err = s.db.QueryRow(`SELECT name, token, meta_json FROM nodes WHERE id = ?`, id).Scan(&name, &token, &metaJSON)
	if err != nil {
		return
	}
	_ = json.Unmarshal([]byte(metaJSON), &meta)
	return
}

func (s *Store) ListNodesAdmin() ([]map[string]any, error) {
	rows, err := s.db.Query(`SELECT id, name, token, meta_json, created_at FROM nodes ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	for rows.Next() {
		var id, name, token, metaJSON, createdStr string
		if err := rows.Scan(&id, &name, &token, &metaJSON, &createdStr); err != nil {
			return nil, err
		}
		var meta models.NodeMeta
		_ = json.Unmarshal([]byte(metaJSON), &meta)
		online := false
		var lastSeen time.Time
		if t, ok := s.seen[id]; ok {
			lastSeen = t
			online = now.Sub(t) < offlineAfter
		}
		out = append(out, map[string]any{
			"id":         id,
			"name":       name,
			"token":      token,
			"meta":       meta,
			"created_at": createdStr,
			"online":     online,
			"last_seen":  lastSeen,
			"metrics":    s.metrics[id],
		})
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out, nil
}
