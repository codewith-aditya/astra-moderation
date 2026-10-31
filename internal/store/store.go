// Package store is a small, dependency-free persistence layer: tenants are
// kept in a JSON file, moderation events are appended to JSONL. It is designed
// for single-node deployments and is safe for concurrent use.
package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/codewith-aditya/astra-moderation/internal/auth"
	"github.com/codewith-aditya/astra-moderation/internal/policy"
)

// Tenant is an API consumer. Policy optionally overrides global thresholds.
type Tenant struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	KeyHash   string         `json:"key_hash"`
	RPM       int            `json:"rpm"`
	CreatedAt time.Time      `json:"created_at"`
	Disabled  bool           `json:"disabled"`
	Policy    *policy.Policy `json:"policy,omitempty"`
}

// Event is one moderation outcome, appended to events.jsonl.
type Event struct {
	At        time.Time          `json:"at"`
	TenantID  string             `json:"tenant_id"`
	Kind      string             `json:"kind"`
	Verdict   string             `json:"verdict"`
	LatencyMS int64              `json:"latency_ms"`
	Flags     []policy.Flag      `json:"flags,omitempty"`
	Scores    map[string]float64 `json:"scores,omitempty"`
}

// Summary aggregates events for a tenant.
type Summary struct {
	Total  int            `json:"total"`
	Images int            `json:"images"`
	Videos int            `json:"videos"`
	Safe   int            `json:"safe"`
	Review int            `json:"review"`
	Block  int            `json:"block"`
	ByDay  map[string]int `json:"by_day"`
}

// Store is the persistence root.
type Store struct {
	dir     string
	mu      sync.RWMutex
	tenants map[string]*Tenant
	byHash  map[string]*Tenant
}

// Open loads tenants from dir/tenants.json (creating the file when missing).
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	s := &Store{dir: dir, tenants: map[string]*Tenant{}, byHash: map[string]*Tenant{}}
	path := filepath.Join(dir, "tenants.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("store: read tenants: %w", err)
	}
	var list []*Tenant
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("store: parse tenants: %w", err)
	}
	for _, t := range list {
		s.tenants[t.ID] = t
		s.byHash[t.KeyHash] = t
	}
	return s, nil
}

func (s *Store) persistLocked() error {
	list := make([]*Tenant, 0, len(s.tenants))
	for _, t := range s.tenants {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, "tenants.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// CreateTenant generates a key and persists the tenant. The plaintext key is
// returned once and never stored.
func (s *Store) CreateTenant(name string, rpm int) (*Tenant, string, error) {
	if name == "" {
		return nil, "", fmt.Errorf("store: tenant name required")
	}
	plain, hash, err := auth.GenerateKey()
	if err != nil {
		return nil, "", err
	}
	t := &Tenant{
		ID:        fmt.Sprintf("t_%d", time.Now().UnixNano()),
		Name:      name,
		KeyHash:   hash,
		RPM:       rpm,
		CreatedAt: time.Now().UTC(),
	}
	s.mu.Lock()
	s.tenants[t.ID] = t
	s.byHash[t.KeyHash] = t
	err = s.persistLocked()
	s.mu.Unlock()
	if err != nil {
		return nil, "", err
	}
	return t, plain, nil
}

// GetByHash resolves a tenant by key hash.
func (s *Store) GetByHash(hash string) (*Tenant, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.byHash[hash]
	if !ok || t.Disabled {
		return nil, false
	}
	return t, true
}

// GetByID resolves a tenant by ID (disabled tenants included).
func (s *Store) GetByID(id string) (*Tenant, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tenants[id]
	return t, ok
}

// List returns all tenants sorted by creation time.
func (s *Store) List() []*Tenant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Tenant, 0, len(s.tenants))
	for _, t := range s.tenants {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// SetDisabled toggles a tenant.
func (s *Store) SetDisabled(id string, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tenants[id]
	if !ok {
		return fmt.Errorf("store: tenant %q not found", id)
	}
	t.Disabled = disabled
	return s.persistLocked()
}

// SetPolicy sets (or clears, with nil) a tenant policy override.
func (s *Store) SetPolicy(id string, p *policy.Policy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tenants[id]
	if !ok {
		return fmt.Errorf("store: tenant %q not found", id)
	}
	t.Policy = p
	return s.persistLocked()
}

// RecordEvent appends one moderation event to events.jsonl.
func (s *Store) RecordEvent(e Event) error {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	f, err := os.OpenFile(filepath.Join(s.dir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(raw)
	return err
}

// scanEvents reads events.jsonl line by line.
func (s *Store) scanEvents(fn func(Event) bool) error {
	f, err := os.Open(filepath.Join(s.dir, "events.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue
		}
		if !fn(e) {
			break
		}
	}
	return sc.Err()
}

// Summary aggregates events since the given time (zero time = all time).
func (s *Store) Summary(tenantID string, since time.Time) (Summary, error) {
	sum := Summary{ByDay: map[string]int{}}
	err := s.scanEvents(func(e Event) bool {
		if tenantID != "" && e.TenantID != tenantID {
			return true
		}
		if !since.IsZero() && e.At.Before(since) {
			return true
		}
		sum.Total++
		switch e.Kind {
		case "video":
			sum.Videos++
		default:
			sum.Images++
		}
		switch e.Verdict {
		case "block":
			sum.Block++
		case "review":
			sum.Review++
		default:
			sum.Safe++
		}
		sum.ByDay[e.At.UTC().Format("2006-01-02")]++
		return true
	})
	return sum, err
}

// RecentEvents returns up to limit newest events for a tenant ("" = all).
func (s *Store) RecentEvents(tenantID string, limit int) ([]Event, error) {
	var all []Event
	err := s.scanEvents(func(e Event) bool {
		if tenantID == "" || e.TenantID == tenantID {
			all = append(all, e)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	// newest first
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	return all, nil
}
