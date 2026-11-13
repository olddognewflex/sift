package ledger

import (
	"fmt"
	"time"
)

// Entry is one change to one account's balance.
type Entry struct {
	Account string    `json:"account"`
	Amount  int64     `json:"amount"`
	Balance int64     `json:"balance"` // the account balance after this entry
	Ref     string    `json:"ref,omitempty"`
	At      time.Time `json:"at"`
}

// Statement is an account's entries, oldest first, with its balance before
// the first entry and after the last.
type Statement struct {
	Account Account `json:"account"`
	Opening int64   `json:"opening"`
	Closing int64   `json:"closing"`
	Entries []Entry `json:"entries"`
}

// Statement returns the statement for an account. It reports ErrCorrupt if
// the recorded entries do not add up to the account's balance.
func (s *Store) Statement(id string) (Statement, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.accounts[id]
	if !ok {
		return Statement{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	e := []Entry{}
	for _, x := range s.entries {
		if x.Account == id {
			e = append(e, x)
		}
	}
	st := Statement{Account: *a, Opening: a.Balance, Closing: a.Balance, Entries: e}
	if len(e) == 0 {
		return st, nil
	}
	if !continuous(e) || e[len(e)-1].Balance != a.Balance {
		return Statement{}, fmt.Errorf("%w: %s", ErrCorrupt, id)
	}
	st.Opening = e[0].Balance - e[0].Amount
	return st, nil
}

// continuous reports whether each entry's balance follows from the balance
// recorded by the entry before it.
func continuous(e []Entry) bool {
	for i := 0; i < len(e)-1; i++ {
		if e[i].Balance+e[i+1].Amount != e[i+1].Balance {
			return false
		}
	}
	return true
}
