package store

import (
	"testing"
	"time"

	"github.com/codewith-aditya/astra-moderation/internal/auth"
	"github.com/codewith-aditya/astra-moderation/internal/policy"
)

func TestCreateAndResolveTenant(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tenant, plain, err := st.CreateTenant("acme", 60)
	if err != nil {
		t.Fatal(err)
	}
	if plain == "" || tenant.ID == "" {
		t.Fatal("empty key or id")
	}
	if _, ok := st.GetByHash(auth.Hash(plain)); !ok {
		t.Fatal("tenant not resolvable by key hash")
	}
	if _, ok := st.GetByHash(auth.Hash("amk_wrong")); ok {
		t.Fatal("bogus hash resolved")
	}
}

func TestDisabledTenantNotResolvable(t *testing.T) {
	st, _ := Open(t.TempDir())
	tenant, plain, _ := st.CreateTenant("acme", 60)
	if err := st.SetDisabled(tenant.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.GetByHash(auth.Hash(plain)); ok {
		t.Fatal("disabled tenant must not resolve")
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	_, plain, _ := st.CreateTenant("persisted", 30)

	st2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st2.GetByHash(auth.Hash(plain)); !ok {
		t.Fatal("tenant did not survive reopen")
	}
}

func TestEventsAndSummary(t *testing.T) {
	st, _ := Open(t.TempDir())
	tenant, _, _ := st.CreateTenant("acme", 60)
	events := []Event{
		{TenantID: tenant.ID, Kind: "image", Verdict: "safe", At: time.Now()},
		{TenantID: tenant.ID, Kind: "video", Verdict: "block", At: time.Now()},
		{TenantID: tenant.ID, Kind: "image", Verdict: "review", At: time.Now()},
		{TenantID: "other", Kind: "image", Verdict: "safe", At: time.Now()},
	}
	for _, e := range events {
		if err := st.RecordEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	sum, err := st.Summary(tenant.ID, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Total != 3 || sum.Images != 2 || sum.Videos != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	if sum.Safe != 1 || sum.Block != 1 || sum.Review != 1 {
		t.Fatalf("verdict split = %+v", sum)
	}
	recent, err := st.RecentEvents(tenant.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Fatalf("recent = %d", len(recent))
	}
}

func TestSetPolicyOverride(t *testing.T) {
	st, _ := Open(t.TempDir())
	tenant, _, _ := st.CreateTenant("acme", 60)
	p := policy.Default()
	if err := st.SetPolicy(tenant.ID, &p); err != nil {
		t.Fatal(err)
	}
	got, ok := st.GetByID(tenant.ID)
	if !ok || got.Policy == nil {
		t.Fatal("policy override missing")
	}
	if err := st.SetPolicy(tenant.ID, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetByID(tenant.ID)
	if got.Policy != nil {
		t.Fatal("policy override not cleared")
	}
}
