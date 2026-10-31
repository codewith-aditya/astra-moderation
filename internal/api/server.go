// Package api wires HTTP handlers, middleware and dependencies together.
package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/codewith-aditya/astra-moderation/internal/config"
	"github.com/codewith-aditya/astra-moderation/internal/jobs"
	"github.com/codewith-aditya/astra-moderation/internal/metrics"
	"github.com/codewith-aditya/astra-moderation/internal/policy"
	"github.com/codewith-aditya/astra-moderation/internal/ratelimit"
	"github.com/codewith-aditya/astra-moderation/internal/store"
	"github.com/codewith-aditya/astra-moderation/internal/upstream"
)

// Version is stamped at build time (see Makefile / Dockerfile).
var Version = "dev"

// Server holds every dependency the handlers need.
type Server struct {
	cfg     config.Config
	pol     policy.Policy
	store   *store.Store
	rl      *ratelimit.Limiter
	up      *upstream.Client
	jobs    *jobs.Manager
	m       *metrics.Metrics
	log     *slog.Logger
	started time.Time
}

// New constructs a Server.
func New(cfg config.Config, pol policy.Policy, st *store.Store, up *upstream.Client, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		cfg:     cfg,
		pol:     pol,
		store:   st,
		rl:      ratelimit.New(),
		up:      up,
		jobs:    jobs.NewManager(cfg.JobTTL),
		m:       metrics.New(),
		log:     logger,
		started: time.Now(),
	}
}

// Metrics exposes the registry (used by the janitor/main for tests).
func (s *Server) Metrics() *metrics.Metrics { return s.m }

// Jobs exposes the job manager.
func (s *Server) Jobs() *jobs.Manager { return s.jobs }

// Limiter exposes the rate limiter so main can run GC.
func (s *Server) Limiter() *ratelimit.Limiter { return s.rl }

// Handler builds the full routing table.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("GET /v1/categories", s.handleCategories)
	mux.HandleFunc("GET /", s.handleIndex)

	mux.Handle("POST /v1/moderate", s.requireKey(http.HandlerFunc(s.handleModerate)))
	mux.Handle("POST /v1/moderate/batch", s.requireKey(http.HandlerFunc(s.handleBatch)))
	mux.Handle("POST /v1/moderations", s.requireKey(http.HandlerFunc(s.handleOpenAIModerations)))
	mux.Handle("GET /v1/usage", s.requireKey(http.HandlerFunc(s.handleUsage)))
	mux.Handle("POST /v1/jobs", s.requireKey(http.HandlerFunc(s.handleCreateJob)))
	mux.Handle("GET /v1/jobs/{id}", s.requireKey(http.HandlerFunc(s.handleGetJob)))

	mux.Handle("POST /admin/keys", s.requireAdmin(http.HandlerFunc(s.handleCreateKey)))
	mux.Handle("GET /admin/keys", s.requireAdmin(http.HandlerFunc(s.handleListKeys)))
	mux.Handle("POST /admin/keys/{id}/disable", s.requireAdmin(http.HandlerFunc(s.handleToggleKey)))
	mux.Handle("POST /admin/keys/{id}/enable", s.requireAdmin(http.HandlerFunc(s.handleToggleKey)))
	mux.Handle("POST /admin/keys/{id}/policy", s.requireAdmin(http.HandlerFunc(s.handleSetPolicy)))
	mux.Handle("GET /admin/usage", s.requireAdmin(http.HandlerFunc(s.handleAdminUsage)))
	mux.Handle("GET /admin/events", s.requireAdmin(http.HandlerFunc(s.handleAdminEvents)))

	return s.withMiddleware(mux)
}
