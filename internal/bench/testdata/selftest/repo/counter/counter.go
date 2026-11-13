// Package counter counts events per key.
package counter

import (
	"sort"
	"sync"
)

// Counter counts events per key. It is safe for concurrent use.
type Counter struct {
	mu     sync.Mutex
	counts map[string]int
}

// New returns an empty Counter.
func New() *Counter {
	return &Counter{counts: map[string]int{}}
}

// Add records n events for key.
func (c *Counter) Add(key string, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[key] += n
}

// Get returns the number of events recorded for key.
func (c *Counter) Get(key string) int {
	return c.counts[key]
}

// Top returns up to n keys with the highest counts, highest first. Ties
// are broken by key.
func (c *Counter) Top(n int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n <= 0 {
		return nil
	}
	if n > len(c.counts) {
		n = len(c.counts)
	}
	keys := make([]string, 0, len(c.counts))
	for k := range c.counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if c.counts[keys[i]] != c.counts[keys[j]] {
			return c.counts[keys[i]] > c.counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys[:n]
}
