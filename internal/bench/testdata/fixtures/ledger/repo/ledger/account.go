// Package ledger keeps account balances and moves money between accounts.
package ledger

import (
	"errors"
	"time"
)

// Account is a single ledger account. Balances are in minor units (cents),
// so all arithmetic is exact.
type Account struct {
	ID       string    `json:"id"`
	Owner    string    `json:"owner"`
	Currency string    `json:"currency"`
	Balance  int64     `json:"balance"`
	Closed   bool      `json:"closed"`
	Opened   time.Time `json:"opened"`
}

var (
	ErrNotFound          = errors.New("account not found")
	ErrExists            = errors.New("account already exists")
	ErrInvalidAccount    = errors.New("account id and currency are required")
	ErrClosed            = errors.New("account is closed")
	ErrNonZeroBalance    = errors.New("account balance is not zero")
	ErrInvalidAmount     = errors.New("amount must be positive")
	ErrSameAccount       = errors.New("source and destination are the same account")
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrCurrencyMismatch  = errors.New("currencies differ")
	ErrCorrupt           = errors.New("ledger entries are inconsistent")
)
