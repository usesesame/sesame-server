package httpapi

import (
	"strconv"
	"sync"
	"time"
)

const (
	maxRememberedRefusals = 10000
	refusalSweepInterval  = time.Second
	syncFlagKey           = "cloud_sync_available"
)

var syncFlagCacheDuration = 5 * time.Second

type refusals struct {
	mu        sync.Mutex
	until     map[string]time.Time
	nextSweep time.Time
}

func newRefusals() *refusals {
	return &refusals{until: map[string]time.Time{}}
}

func refusalKey(key string, limit int, window time.Duration) string {
	return strconv.Itoa(limit) + "/" + strconv.FormatInt(int64(window), 10) + "/" + key
}

func (r *refusals) active(key string, now time.Time) (time.Duration, bool) {
	if r == nil {
		return 0, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	until, found := r.until[key]
	if !found {
		return 0, false
	}
	if !now.Before(until) {
		delete(r.until, key)
		return 0, false
	}
	return until.Sub(now), true
}

func (r *refusals) remember(key string, until, now time.Time) {
	if r == nil || !now.Before(until) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, found := r.until[key]; !found && len(r.until) >= maxRememberedRefusals {
		r.sweep(now)
		if len(r.until) >= maxRememberedRefusals {
			return
		}
	}
	r.until[key] = until
}

func (r *refusals) sweep(now time.Time) {
	if now.Before(r.nextSweep) {
		return
	}
	r.nextSweep = now.Add(refusalSweepInterval)
	for key, until := range r.until {
		if !now.Before(until) {
			delete(r.until, key)
		}
	}
}

func (r *refusals) size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.until)
}

type syncFlagCache struct {
	mu         sync.Mutex
	enabled    bool
	expires    time.Time
	generation uint64
}

func (c *syncFlagCache) read(now time.Time, load func() (enabled, unreadable bool)) (bool, bool) {
	c.mu.Lock()
	if now.Before(c.expires) {
		enabled := c.enabled
		c.mu.Unlock()
		return enabled, false
	}
	generation := c.generation
	c.mu.Unlock()
	enabled, unreadable := load()
	if unreadable {
		return false, true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation == c.generation {
		c.enabled = enabled
		c.expires = now.Add(syncFlagCacheDuration)
	}
	return enabled, false
}

func (c *syncFlagCache) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	c.expires = time.Time{}
}
