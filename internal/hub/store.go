package hub

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/FengBujue0104/tanzhen/internal/models"
	_ "modernc.org/sqlite"
)

// Defaults for tunables. Overridden by config.
const (
	DefaultOfflineAfter = 30 * time.Second
	DefaultHistoryCap   = 60
	DefaultPersistEvery = 15 * time.Second
	DefaultRetention    = 2 * time.Hour
)

type Store struct {
	db *sql.DB
	mu sync.RWMutex

	// In-memory live state. Metrics themselves are not persisted: a hub restart
	// waits for the next heartbeat. History is downsampled into SQLite and
	// rehydrated on open.
	metrics     map[string]*models.Heartbeat
	seen        map[string]time.Time
	history     map[string][]models.Sample
	lastPersist map[string]int64 // unix seconds of last SQLite sample per node

	offlineAfter time.Duration
	historyCap   int
	persistEvery time.Duration
	retention    time.Duration

	now func() time.Time
}

func NewStore(path string, opts StoreOptions) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	// modernc's sqlite is a C translation; capping the pool keeps WAL happy.
	db.SetMaxOpenConns(4)
	db.SetConnMaxLifetime(0)

	s := &Store{
		db:           db,
		metrics:      make(map[string]*models.Heartbeat),
		seen:         make(map[string]time.Time),
		history:      make(map[string][]models.Sample),
		lastPersist:  make(map[string]int64),
		offlineAfter: opts.OfflineAfter,
		historyCap:   opts.HistoryCap,
		persistEvery: opts.PersistEvery,
		retention:    opts.Retention,
		now:          time.Now,
	}
	if s.offlineAfter <= 0 {
		s.offlineAfter = DefaultOfflineAfter
	}
	if s.historyCap <= 0 {
		s.historyCap = DefaultHistoryCap
	}
	if s.persistEvery <= 0 {
		s.persistEvery = DefaultPersistEvery
	}
	if s.retention <= 0 {
		s.retention = DefaultRetention
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.hydrateHistory(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// StoreOptions tunes live-state behaviour.
type StoreOptions struct {
	OfflineAfter time.Duration
	HistoryCap   int
	PersistEvery time.Duration
	Retention    time.Duration
}

func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS nodes (
	  id TEXT PRIMARY KEY,
	  name TEXT NOT NULL,
	  token TEXT NOT NULL UNIQUE,
	  meta_json TEXT NOT NULL DEFAULT '{}',
	  traffic_json TEXT NOT NULL DEFAULT '{}',
	  created_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_nodes_token ON nodes(token);
	`); err != nil {
		return err
	}
	// v1 databases lack traffic_json.
	var has int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('nodes') WHERE name = 'traffic_json'`).Scan(&has)
	if has == 0 {
		if _, err := s.db.Exec(`ALTER TABLE nodes ADD COLUMN traffic_json TEXT NOT NULL DEFAULT '{}'`); err != nil {
			return fmt.Errorf("migrate traffic_json: %w", err)
		}
	}
	if _, err := s.db.Exec(`
	CREATE TABLE IF NOT EXISTS samples (
	  node_id TEXT NOT NULL,
	  t INTEGER NOT NULL,
	  cpu REAL NOT NULL DEFAULT 0,
	  mem REAL NOT NULL DEFAULT 0,
	  up INTEGER NOT NULL DEFAULT 0,
	  down INTEGER NOT NULL DEFAULT 0,
	  lat_ct REAL NOT NULL DEFAULT -1,
	  lat_cu REAL NOT NULL DEFAULT -1,
	  lat_cm REAL NOT NULL DEFAULT -1,
	  loss_ct REAL NOT NULL DEFAULT -1,
	  loss_cu REAL NOT NULL DEFAULT -1,
	  loss_cm REAL NOT NULL DEFAULT -1,
	  UNIQUE(node_id, t)
	);
	CREATE INDEX IF NOT EXISTS idx_samples_node_t ON samples(node_id, t);
	`); err != nil {
		return fmt.Errorf("migrate samples: %w", err)
	}
	return nil
}

// CreateNode inserts a node and returns its id and one-time token.
func (s *Store) CreateNode(name string, meta models.NodeMeta) (id, token string, err error) {
	id = randHex(8)
	token = randHex(24)
	if name == "" {
		name = "节点-" + id[:6]
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return "", "", err
	}
	_, err = s.db.Exec(
		`INSERT INTO nodes (id, name, token, meta_json, traffic_json, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, token, string(metaJSON), "{}", time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return "", "", err
	}
	// No baseline is written here. A node is created before its first heartbeat,
	// and the hub's cumulative counters are meaningless until then — the first
	// heartbeat anchors the period instead. Writing a zero baseline at creation
	// would charge the host's entire since-boot traffic to the first quota.
	return id, token, nil
}

// DeleteNode removes a node, its live state, and its persisted samples.
func (s *Store) DeleteNode(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`DELETE FROM nodes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.Exec(`DELETE FROM samples WHERE node_id = ?`, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.metrics, id)
	delete(s.seen, id)
	delete(s.history, id)
	delete(s.lastPersist, id)
	s.mu.Unlock()
	return nil
}

// UpdateNode applies a partial update. Changing the traffic period re-anchors
// the accounting baseline so the new quota starts counting from now.
func (s *Store) UpdateNode(id string, name *string, meta *models.NodeMeta) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var curName, metaJSON, trafficJSON string
	row := tx.QueryRow(`SELECT name, meta_json, traffic_json FROM nodes WHERE id = ?`, id)
	if err := row.Scan(&curName, &metaJSON, &trafficJSON); err != nil {
		return sql.ErrNoRows
	}
	var curMeta models.NodeMeta
	_ = json.Unmarshal([]byte(metaJSON), &curMeta)
	periodChanged := meta != nil && curMeta.TrafficPeriod != meta.TrafficPeriod

	if name != nil && *name != "" {
		curName = *name
	}
	if meta != nil {
		curMeta = *meta
	}
	b, err := json.Marshal(curMeta)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE nodes SET name = ?, meta_json = ? WHERE id = ?`, curName, string(b), id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if periodChanged {
		s.SetTrafficBaseline(id, curMeta.TrafficPeriod)
	}
	return nil
}

// NodeByToken resolves an agent token and returns its node metadata so the
// heartbeat path does not need a second query.
func (s *Store) NodeByToken(token string) (id, name string, meta models.NodeMeta, err error) {
	var metaJSON string
	err = s.db.QueryRow(`SELECT id, name, meta_json FROM nodes WHERE token = ?`, token).Scan(&id, &name, &metaJSON)
	if err != nil {
		return "", "", models.NodeMeta{}, err
	}
	_ = json.Unmarshal([]byte(metaJSON), &meta)
	return id, name, meta, nil
}

// SaveHeartbeat records live metrics and rolls the traffic accounting forward.
func (s *Store) SaveHeartbeat(id string, hb *models.Heartbeat, meta models.NodeMeta) {
	now := s.clock()
	sm := sampleFrom(hb, now.Unix())

	s.mu.Lock()
	cp := *hb
	s.metrics[id] = &cp
	s.seen[id] = now
	s.pushHistory(id, sm)
	persist := s.dueToPersistLocked(id, sm.T)
	if persist {
		s.lastPersist[id] = sm.T
	}
	s.mu.Unlock()

	if persist {
		s.insertSample(id, sm)
		s.PruneSamples()
	}

	if meta.TrafficPeriod > 0 {
		s.rollTraffic(id, hb, meta.TrafficPeriod)
	}
}

func sampleFrom(hb *models.Heartbeat, t int64) models.Sample {
	return models.Sample{
		T:      t,
		CPU:    hb.CPUUsage,
		Mem:    hb.MemUsage,
		Up:     hb.NetUp,
		Down:   hb.NetDown,
		LatCT:  hb.LatencyCT,
		LatCU:  hb.LatencyCU,
		LatCM:  hb.LatencyCM,
		LossCT: hb.LossCT,
		LossCU: hb.LossCU,
		LossCM: hb.LossCM,
	}
}

// dueToPersistLocked reports whether smT is far enough after the last persisted
// sample. Caller holds s.mu.
func (s *Store) dueToPersistLocked(id string, smT int64) bool {
	last, ok := s.lastPersist[id]
	if !ok {
		return true
	}
	if smT <= last {
		return false
	}
	return time.Duration(smT-last)*time.Second >= s.persistEvery
}

func (s *Store) pushHistory(id string, sm models.Sample) {
	h := s.history[id]
	if len(h) >= s.historyCap {
		if len(h) > s.historyCap {
			h = h[len(h)-s.historyCap:]
		}
		copy(h, h[1:])
		h[len(h)-1] = sm
		s.history[id] = h
		return
	}
	s.history[id] = append(h, sm)
}

func (s *Store) insertSample(id string, sm models.Sample) {
	_, _ = s.db.Exec(`INSERT OR REPLACE INTO samples
		(node_id, t, cpu, mem, up, down, lat_ct, lat_cu, lat_cm, loss_ct, loss_cu, loss_cm)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, sm.T, sm.CPU, sm.Mem, sm.Up, sm.Down,
		sm.LatCT, sm.LatCU, sm.LatCM, sm.LossCT, sm.LossCU, sm.LossCM)
}

// PruneSamples drops SQLite samples older than HISTORY_RETENTION.
func (s *Store) PruneSamples() {
	cutoff := s.clock().Add(-s.retention).Unix()
	_, _ = s.db.Exec(`DELETE FROM samples WHERE t < ?`, cutoff)
}

func (s *Store) hydrateHistory() error {
	rows, err := s.db.Query(`
		SELECT node_id, t, cpu, mem, up, down, lat_ct, lat_cu, lat_cm, loss_ct, loss_cu, loss_cm
		FROM (
		  SELECT node_id, t, cpu, mem, up, down, lat_ct, lat_cu, lat_cm, loss_ct, loss_cu, loss_cm,
		         ROW_NUMBER() OVER (PARTITION BY node_id ORDER BY t DESC) AS rn
		  FROM samples
		)
		WHERE rn <= ?
		ORDER BY node_id, t`, s.historyCap)
	if err != nil {
		return fmt.Errorf("hydrate history: %w", err)
	}
	defer rows.Close()

	s.mu.Lock()
	defer s.mu.Unlock()
	for rows.Next() {
		var id string
		var sm models.Sample
		var up, down int64
		if err := rows.Scan(&id, &sm.T, &sm.CPU, &sm.Mem, &up, &down,
			&sm.LatCT, &sm.LatCU, &sm.LatCM, &sm.LossCT, &sm.LossCU, &sm.LossCM); err != nil {
			return err
		}
		sm.Up = uint64(up)
		sm.Down = uint64(down)
		s.history[id] = append(s.history[id], sm)
		s.lastPersist[id] = sm.T
	}
	return rows.Err()
}

// HasNode reports whether a node id exists.
func (s *Store) HasNode(id string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE id = ?`, id).Scan(&n)
	return n > 0, err
}

// Retention returns the configured sample retention window.
func (s *Store) Retention() time.Duration { return s.retention }

// ListHistory returns persisted samples in [from, to] (unix seconds, inclusive).
func (s *Store) ListHistory(id string, from, to int64) ([]models.Sample, error) {
	rows, err := s.db.Query(`
		SELECT t, cpu, mem, up, down, lat_ct, lat_cu, lat_cm, loss_ct, loss_cu, loss_cm
		FROM samples WHERE node_id = ? AND t >= ? AND t <= ? ORDER BY t`, id, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Sample{}
	for rows.Next() {
		var sm models.Sample
		var up, down int64
		if err := rows.Scan(&sm.T, &sm.CPU, &sm.Mem, &up, &down,
			&sm.LatCT, &sm.LatCU, &sm.LatCM, &sm.LossCT, &sm.LossCU, &sm.LossCM); err != nil {
			return nil, err
		}
		sm.Up = uint64(up)
		sm.Down = uint64(down)
		out = append(out, sm)
	}
	return out, rows.Err()
}

// rollTraffic advances a node's period, re-baselining the counters on rollover.
func (s *Store) rollTraffic(id string, hb *models.Heartbeat, period int) {
	st := s.loadTraffic(id)
	now := time.Now()
	changed := false
	if st.ResetAt.IsZero() || !now.Before(st.ResetAt) {
		st.BaseUp, st.BaseDown = hb.NetTotalUp, hb.NetTotalDown
		st.ResetAt = advanceReset(st.ResetAt, period, now)
		changed = true
	}
	if !changed {
		return
	}
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	_, _ = s.db.Exec(`UPDATE nodes SET traffic_json = ? WHERE id = ?`, string(b), id)
}

func (s *Store) loadTraffic(id string) models.TrafficState {
	var raw string
	err := s.db.QueryRow(`SELECT traffic_json FROM nodes WHERE id = ?`, id).Scan(&raw)
	if err != nil || raw == "" || raw == "{}" {
		return models.TrafficState{}
	}
	var st models.TrafficState
	if json.Unmarshal([]byte(raw), &st) != nil {
		return models.TrafficState{}
	}
	return st
}

// SetTrafficBaseline anchors a node's counters to "now". Called when the admin
// changes the traffic period so the quota is not charged retroactively.
//
// A node that has not reported yet is cleared rather than anchored: its counters
// do not exist, so an anchored zero baseline would bill the first heartbeat for
// everything the host has moved since boot.
func (s *Store) SetTrafficBaseline(id string, period int) {
	if period <= 0 {
		return
	}
	s.mu.RLock()
	hb := s.metrics[id]
	s.mu.RUnlock()

	if hb == nil {
		_, _ = s.db.Exec(`UPDATE nodes SET traffic_json = '{}' WHERE id = ?`, id)
		return
	}
	st := models.TrafficState{
		BaseUp:   hb.NetTotalUp,
		BaseDown: hb.NetTotalDown,
		ResetAt:  advanceReset(time.Time{}, period, time.Now()),
	}
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	_, _ = s.db.Exec(`UPDATE nodes SET traffic_json = ? WHERE id = ?`, string(b), id)
}

// advananceReset walks prev forward to the first boundary strictly after now.
// A zero prev means "start a fresh period from now".
func advanceReset(prev time.Time, period int, now time.Time) time.Time {
	if period <= 0 {
		return time.Time{}
	}
	if prev.IsZero() {
		return now.AddDate(0, 0, period)
	}
	n := prev
	for i := 0; i < 100000; i++ {
		n = n.AddDate(0, 0, period)
		if n.After(now) {
			return n
		}
	}
	return now.AddDate(0, 0, period)
}

// counterDelta subtracts a baseline, treating a decrease as a host-side reset.
func counterDelta(cur, base uint64) uint64 {
	if cur < base {
		return cur
	}
	return cur - base
}

// computeTraffic derives the traffic view for a node.
func (s *Store) computeTraffic(id string, meta models.NodeMeta, hb *models.Heartbeat) models.Traffic {
	t := models.Traffic{Quota: meta.TrafficQuota, PeriodDays: meta.TrafficPeriod, Unlimited: true, Remaining: -1, Pct: -1}
	// No period means no reset boundary to measure "used this period" against,
	// so the only honest figure is the cumulative one the front end reads off
	// the heartbeat counters directly.
	if meta.TrafficPeriod <= 0 {
		return t
	}
	st := s.loadTraffic(id)
	if st.ResetAt.IsZero() || hb == nil {
		// The period IS configured; only the accounting baseline is missing
		// because no heartbeat has landed yet. Report the quota with zero usage
		// rather than claiming nothing was set up.
		t.Unlimited = false
		t.Pct = 0
		if meta.TrafficQuota > 0 {
			t.Remaining = int64(meta.TrafficQuota)
		}
		return t
	}
	t.Unlimited = false
	t.ResetAt = &st.ResetAt
	t.UsedUp = counterDelta(hb.NetTotalUp, st.BaseUp)
	t.UsedDown = counterDelta(hb.NetTotalDown, st.BaseDown)
	t.Used = t.UsedUp + t.UsedDown
	if meta.TrafficQuota > 0 {
		t.Remaining = int64(meta.TrafficQuota) - int64(t.Used)
		if t.Remaining < 0 {
			t.Remaining = 0
		}
		t.Pct = float64(t.Used) / float64(meta.TrafficQuota) * 100
		if t.Pct > 100 {
			t.Pct = 100
		}
	}
	return t
}

// ListStatus returns the public node view.
func (s *Store) ListStatus() ([]models.NodeStatus, error) {
	rows, err := s.db.Query(`SELECT id, name, meta_json, created_at FROM nodes ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	out := []models.NodeStatus{}
	for rows.Next() {
		var id, name, metaJSON, createdStr string
		if err := rows.Scan(&id, &name, &metaJSON, &createdStr); err != nil {
			return nil, err
		}
		var meta models.NodeMeta
		_ = json.Unmarshal([]byte(metaJSON), &meta)
		created, _ := time.Parse(time.RFC3339Nano, createdStr)
		st := models.NodeStatus{
			ID:        id,
			Name:      name,
			CreatedAt: created,
			Meta:      meta,
			// Always report the configured quota, even before the first
			// heartbeat: a node that has never reported still has a
			// subscription, and the zero value would read as "none configured".
			Traffic: s.computeTraffic(id, meta, nil),
		}
		if h := s.history[id]; len(h) > 0 {
			st.History = append([]models.Sample(nil), h...)
		}
		if t, ok := s.seen[id]; ok {
			st.LastSeen = t
			st.Online = now.Sub(t) < s.offlineAfter
			if m, ok2 := s.metrics[id]; ok2 {
				cp := *m
				st.Metrics = &cp
				st.Traffic = s.computeTraffic(id, meta, &cp)
			}
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// GetNodeAdmin returns the admin view of one node.
func (s *Store) GetNodeAdmin(id string) (name, token string, meta models.NodeMeta, err error) {
	var metaJSON string
	err = s.db.QueryRow(`SELECT name, token, meta_json FROM nodes WHERE id = ?`, id).Scan(&name, &token, &metaJSON)
	if err != nil {
		return
	}
	_ = json.Unmarshal([]byte(metaJSON), &meta)
	return
}

// ListNodesAdmin returns every node including its token.
func (s *Store) ListNodesAdmin() ([]map[string]any, error) {
	rows, err := s.db.Query(`SELECT id, name, token, meta_json, traffic_json, created_at FROM nodes ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	out := []map[string]any{}
	for rows.Next() {
		var id, name, token, metaJSON, trafficJSON, createdStr string
		if err := rows.Scan(&id, &name, &token, &metaJSON, &trafficJSON, &createdStr); err != nil {
			return nil, err
		}
		var meta models.NodeMeta
		_ = json.Unmarshal([]byte(metaJSON), &meta)
		online := false
		var lastSeen time.Time
		var traffic models.Traffic
		if t, ok := s.seen[id]; ok {
			lastSeen = t
			online = now.Sub(t) < s.offlineAfter
			if m, ok2 := s.metrics[id]; ok2 {
				traffic = s.computeTraffic(id, meta, m)
			}
		}
		out = append(out, map[string]any{
			"id":         id,
			"name":       name,
			"token":      token,
			"meta":       meta,
			"created_at": createdStr,
			"online":     online,
			"last_seen":  lastSeen,
			"traffic":    traffic,
			"metrics":    s.metrics[id],
		})
	}
	return out, rows.Err()
}

// NodeCount reports how many nodes exist (used by tests and diagnostics).
func (s *Store) NodeCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM nodes`).Scan(&n)
	return n, err
}
