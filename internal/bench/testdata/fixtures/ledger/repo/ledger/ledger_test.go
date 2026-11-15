package ledger

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

// fakeClock returns a fixed time until it is advanced.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestStore(t *testing.T) (*Store, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)}
	s, err := NewStore(clock, []OpeningAccount{
		{ID: "alice", Owner: "Alice", Currency: "EUR", Balance: 10_000},
		{ID: "bob", Owner: "Bob", Currency: "EUR", Balance: 500},
		{ID: "carol", Owner: "Carol", Currency: "USD"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, clock
}

func balance(t *testing.T, s *Store, id string) int64 {
	t.Helper()
	a, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return a.Balance
}

func TestTransferMovesFunds(t *testing.T) {
	s, _ := newTestStore(t)
	status, err := s.Transfer("alice", "bob", 2_500, "inv-1")
	if err != nil || status != http.StatusOK {
		t.Fatalf("Transfer: %d %v", status, err)
	}
	if got := balance(t, s, "alice"); got != 7_500 {
		t.Errorf("alice: got %d, want 7500", got)
	}
	if got := balance(t, s, "bob"); got != 3_000 {
		t.Errorf("bob: got %d, want 3000", got)
	}
}

func TestTransferRejects(t *testing.T) {
	for _, tc := range []struct {
		name     string
		from, to string
		amount   int64
		status   int
		err      error
	}{
		{"insufficient funds", "bob", "alice", 501, http.StatusConflict, ErrInsufficientFunds},
		{"currency mismatch", "alice", "carol", 100, http.StatusConflict, ErrCurrencyMismatch},
		{"unknown destination", "alice", "dave", 100, http.StatusNotFound, ErrNotFound},
		{"zero amount", "alice", "bob", 0, http.StatusBadRequest, ErrInvalidAmount},
		{"same account", "alice", "alice", 100, http.StatusBadRequest, ErrSameAccount},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			status, err := s.Transfer(tc.from, tc.to, tc.amount, "")
			if status != tc.status || !errors.Is(err, tc.err) {
				t.Fatalf("got %d %v, want %d %v", status, err, tc.status, tc.err)
			}
			if got := balance(t, s, "alice"); got != 10_000 {
				t.Errorf("alice: got %d, want 10000", got)
			}
			if got := balance(t, s, "bob"); got != 500 {
				t.Errorf("bob: got %d, want 500", got)
			}
		})
	}
}

func TestStatement(t *testing.T) {
	s, clock := newTestStore(t)
	first := clock.Now()
	if _, err := s.Transfer("alice", "bob", 1_000, "a"); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Hour)
	if _, err := s.Transfer("bob", "alice", 300, "b"); err != nil {
		t.Fatal(err)
	}
	st, err := s.Statement("bob")
	if err != nil {
		t.Fatal(err)
	}
	if st.Opening != 500 || st.Closing != 1_200 || len(st.Entries) != 2 {
		t.Fatalf("got opening %d, closing %d, %d entries", st.Opening, st.Closing, len(st.Entries))
	}
	if !st.Entries[0].At.Equal(first) || !st.Entries[1].At.Equal(first.Add(time.Hour)) {
		t.Errorf("entry times: %v, %v", st.Entries[0].At, st.Entries[1].At)
	}
}

func TestNewStoreRejectsDuplicateIDs(t *testing.T) {
	_, err := NewStore(&fakeClock{}, []OpeningAccount{
		{ID: "alice", Currency: "EUR"},
		{ID: "alice", Currency: "EUR"},
	})
	if !errors.Is(err, ErrExists) {
		t.Fatalf("got %v, want ErrExists", err)
	}
}
