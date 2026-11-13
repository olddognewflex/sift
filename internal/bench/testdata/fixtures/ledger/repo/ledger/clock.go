package ledger

import "time"

// Clock supplies the time stamped on new accounts and entries.
type Clock interface {
	Now() time.Time
}

// SystemClock returns a Clock that reads the system time in UTC.
func SystemClock() Clock { return systemClock{} }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }
