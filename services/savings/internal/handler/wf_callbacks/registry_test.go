package wf_callbacks

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

func noopCallback(_ context.Context, _ pgx.Tx, _ Instance) error { return nil }

func TestRegistry_RegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	r.Register("cash_deposit", noopCallback)
	cb, ok := r.Lookup("cash_deposit")
	if !ok {
		t.Fatal("expected cash_deposit to be registered")
	}
	if cb == nil {
		t.Fatal("Lookup returned nil callback")
	}
}

func TestRegistry_Lookup_UnknownKind(t *testing.T) {
	r := NewRegistry()
	_, ok := r.Lookup("does_not_exist")
	if ok {
		t.Error("expected Lookup to return false for unregistered kind")
	}
}

func TestRegistry_Register_PanicsOnEmptyKind(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty process_kind")
		}
	}()
	NewRegistry().Register("", noopCallback)
}

func TestRegistry_Register_PanicsOnNilCallback(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil callback")
		}
	}()
	NewRegistry().Register("cash_deposit", nil)
}

func TestRegistry_Register_PanicsOnDuplicate(t *testing.T) {
	// Double-registration is a copy-paste bug that should crash at
	// boot rather than silently pick a winner. Verify the panic.
	r := NewRegistry()
	r.Register("cash_deposit", noopCallback)
	defer func() {
		if rec := recover(); rec == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	r.Register("cash_deposit", noopCallback)
}

func TestRegistry_Kinds_ListsEverythingRegistered(t *testing.T) {
	r := NewRegistry()
	r.Register("cash_deposit", noopCallback)
	r.Register("share_purchase", noopCallback)
	r.Register("loan_repayment", noopCallback)

	kinds := r.Kinds()
	if len(kinds) != 3 {
		t.Fatalf("expected 3 kinds, got %d: %v", len(kinds), kinds)
	}
	seen := map[string]bool{}
	for _, k := range kinds {
		seen[k] = true
	}
	for _, want := range []string{"cash_deposit", "share_purchase", "loan_repayment"} {
		if !seen[want] {
			t.Errorf("Kinds() missing %q", want)
		}
	}
}
