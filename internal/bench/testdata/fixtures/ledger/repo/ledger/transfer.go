package ledger

import (
	"errors"
	"fmt"
	"net/http"
)

// Transfer moves amount, in minor units, from one account to another. The
// returned status is the HTTP status that describes the outcome.
func (s *Store) Transfer(from, to string, amount int64, ref string) (int, error) {
	if err := s.transfer(from, to, amount, ref); err != nil {
		return StatusFor(err), err
	}
	return http.StatusOK, nil
}

// StatusFor maps an error returned by the store to an HTTP status.
func StatusFor(err error) int {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrInvalidAmount), errors.Is(err, ErrSameAccount), errors.Is(err, ErrInvalidAccount):
		return http.StatusBadRequest
	case errors.Is(err, ErrExists), errors.Is(err, ErrClosed), errors.Is(err, ErrNonZeroBalance),
		errors.Is(err, ErrInsufficientFunds), errors.Is(err, ErrCurrencyMismatch):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func (s *Store) transfer(from, to string, amount int64, ref string) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	if from == to {
		return ErrSameAccount
	}
	src, err := s.Get(from)
	if err != nil {
		return err
	}
	if src.Balance < amount {
		return fmt.Errorf("%w: %s has %d, needs %d", ErrInsufficientFunds, from, src.Balance, amount)
	}
	dst, err := s.Get(to)
	if err != nil {
		return err
	}
	if dst.Currency != src.Currency {
		return fmt.Errorf("%w: %s is %s, %s is %s", ErrCurrencyMismatch, from, src.Currency, to, dst.Currency)
	}

	if err := s.post(from, -amount, ref); err != nil {
		return err
	}
	if err := s.post(to, amount, ref); err != nil {
		return err
	}
	return nil
}
