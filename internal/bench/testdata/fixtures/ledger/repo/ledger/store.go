package ledger

import (
	"fmt"
	"sort"
	"sync"
)

// Store holds accounts and their entries in memory. It is safe for
// concurrent use.
type Store struct {
	clock Clock

	mu       sync.RWMutex
	accounts map[string]*Account
	entries  []Entry
}

// OpeningAccount is an account that exists when the store is created.
type OpeningAccount struct {
	ID       string `json:"id"`
	Owner    string `json:"owner"`
	Currency string `json:"currency"`
	Balance  int64  `json:"balance"`
}

// NewStore returns a store holding the seed accounts, each stamped as
// opened at the clock's current time. Seed IDs must be unique.
func NewStore(clock Clock, seed []OpeningAccount) (*Store, error) {
	s := &Store{clock: clock, accounts: make(map[string]*Account, len(seed))}
	if err := s.load(seed); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load(seed []OpeningAccount) error {
	now := s.clock.Now()
	for _, a := range seed {
		if a.ID == "" || a.Currency == "" || a.Balance < 0 {
			return fmt.Errorf("seed account %q: id, currency and a non-negative balance are required", a.ID)
		}
		if _, ok := s.accounts[a.ID]; ok {
			return fmt.Errorf("%w: %s", ErrExists, a.ID)
		}
		s.accounts[a.ID] = &Account{ID: a.ID, Owner: a.Owner, Currency: a.Currency, Balance: a.Balance, Opened: now}
	}
	return nil
}

// Get returns a copy of the account with the given ID.
func (s *Store) Get(id string) (Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.accounts[id]
	if !ok {
		return Account{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return *a, nil
}

// List returns copies of all accounts, sorted by ID.
func (s *Store) List() []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Open creates an account with a zero balance.
func (s *Store) Open(id, owner, currency string) (Account, error) {
	if id == "" || currency == "" {
		return Account{}, ErrInvalidAccount
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[id]; ok {
		return Account{}, fmt.Errorf("%w: %s", ErrExists, id)
	}
	a := &Account{ID: id, Owner: owner, Currency: currency, Opened: s.clock.Now()}
	s.accounts[id] = a
	return *a, nil
}

// Close closes an account so it can no longer receive or send money. Its
// balance must be zero.
func (s *Store) Close(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if a.Balance != 0 {
		return fmt.Errorf("%w: %s", ErrNonZeroBalance, id)
	}
	a.Closed = true
	return nil
}

// post applies delta to an account's balance and records the entry.
func (s *Store) post(id string, delta int64, ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if a.Closed {
		return fmt.Errorf("%w: %s", ErrClosed, id)
	}
	a.Balance += delta
	s.entries = append(s.entries, Entry{Account: id, Amount: delta, Balance: a.Balance, Ref: ref, At: s.clock.Now()})
	return nil
}
