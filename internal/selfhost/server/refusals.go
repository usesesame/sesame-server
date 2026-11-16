package server

import (
	"sync"
	"time"
)

const (
	maxRememberedRefusals = 10000
	refusalSweepInterval  = time.Second
)

type refusals struct {
	mu        sync.Mutex
	until     map[string]time.Time
	nextSweep time.Time
}

func newRefusals() *refusals {
	return &refusals{until: map[string]time.Time{}}
}

func (r *refusals) active(key string, now time.Time) (time.Duration, bool) {
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
	if !now.Before(until) {
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

func (r *refusals) forget(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.until, key)
}
