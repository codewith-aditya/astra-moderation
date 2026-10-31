// Package jobs implements an in-memory async job manager for video
// moderation, with webhook delivery and TTL cleanup.
package jobs

import (
	"sync"
	"time"

	"github.com/codewith-aditya/astra-moderation/internal/auth"
	"github.com/codewith-aditya/astra-moderation/internal/policy"
	"github.com/codewith-aditya/astra-moderation/internal/upstream"
)

// Status values.
const (
	StatusQueued  = "queued"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

// Job is one async moderation task.
type Job struct {
	ID         string           `json:"id"`
	TenantID   string           `json:"tenant_id"`
	Kind       string           `json:"kind"`
	Status     string           `json:"status"`
	CreatedAt  time.Time        `json:"created_at"`
	FinishedAt *time.Time       `json:"finished_at,omitempty"`
	Verdict    string           `json:"verdict,omitempty"`
	Flags      []policy.Flag    `json:"flags,omitempty"`
	Result     *upstream.Result `json:"result,omitempty"`
	Error      string           `json:"error,omitempty"`
	WebhookURL string           `json:"webhook_url,omitempty"`
	Secret     string           `json:"-"`
}

// Manager stores jobs in memory keyed by ID.
type Manager struct {
	mu   sync.Mutex
	jobs map[string]*Job
	ttl  time.Duration
}

// NewManager creates a manager; ttl <= 0 means 1 hour.
func NewManager(ttl time.Duration) *Manager {
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &Manager{jobs: map[string]*Job{}, ttl: ttl}
}

// Create registers a queued job and returns it with a webhook signing secret.
func (m *Manager) Create(tenantID, kind, webhookURL string) (*Job, error) {
	secret, err := auth.RandomSecret()
	if err != nil {
		return nil, err
	}
	id := "job_" + secret[:12]
	j := &Job{
		ID:         id,
		TenantID:   tenantID,
		Kind:       kind,
		Status:     StatusQueued,
		CreatedAt:  time.Now().UTC(),
		WebhookURL: webhookURL,
		Secret:     secret,
	}
	m.mu.Lock()
	m.jobs[id] = j
	m.mu.Unlock()
	return j, nil
}

// SetRunning marks a job as running.
func (m *Manager) SetRunning(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j, ok := m.jobs[id]; ok {
		j.Status = StatusRunning
	}
}

// Complete stores the moderation outcome.
func (m *Manager) Complete(id, verdict string, flags []policy.Flag, res *upstream.Result) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return
	}
	now := time.Now().UTC()
	j.Status = StatusDone
	j.Verdict = verdict
	j.Flags = flags
	j.Result = res
	j.FinishedAt = &now
}

// Fail marks a job failed.
func (m *Manager) Fail(id, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return
	}
	now := time.Now().UTC()
	j.Status = StatusFailed
	j.Error = msg
	j.FinishedAt = &now
}

// Get returns a job snapshot if it belongs to the tenant.
func (m *Manager) Get(tenantID, id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok || j.TenantID != tenantID {
		return nil, false
	}
	cp := *j
	return &cp, true
}

// GC removes finished jobs older than the TTL. Returns the removed count.
func (m *Manager) GC() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(-m.ttl)
	removed := 0
	for id, j := range m.jobs {
		if j.FinishedAt != nil && j.FinishedAt.Before(cutoff) {
			delete(m.jobs, id)
			removed++
		}
		if j.FinishedAt == nil && j.CreatedAt.Before(cutoff) {
			delete(m.jobs, id)
			removed++
		}
	}
	return removed
}

// Len returns the number of tracked jobs.
func (m *Manager) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.jobs)
}
