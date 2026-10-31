package api

import (
	"time"

	"github.com/codewith-aditya/astra-moderation/internal/policy"
	"github.com/codewith-aditya/astra-moderation/internal/store"
)

// storeEvent converts a moderation outcome into a persisted event.
func storeEvent(tenantID, kind, verdict string, latencyMS int64, flags []policy.Flag, scores map[string]float64) store.Event {
	return store.Event{
		At:        time.Now().UTC(),
		TenantID:  tenantID,
		Kind:      kind,
		Verdict:   verdict,
		LatencyMS: latencyMS,
		Flags:     flags,
		Scores:    scores,
	}
}
