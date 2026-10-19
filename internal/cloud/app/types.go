package app

import (
	"sync"
	"time"

	"uuid"

	"golang.org/x/time/rate"

	"github.com/thekiharani/monitor/internal/cloud/usage"
)

type Principal struct {
	PrincipalID  uuid.UUID
	AccountID    uuid.UUID
	Plan, Status string
	Role         string
	APIKeyID     *uuid.UUID
	SessionID    *uuid.UUID
}

func (p *Principal) SessionIDPtr(s string) {
	id, e := uuid.Parse(s)
	if e == nil {
		p.SessionID = &id
	}
}

func (p *Principal) APIKeyIDPtr(s string) {
	id, e := uuid.Parse(s)
	if e == nil {
		p.APIKeyID = &id
	}
}

type otpRequest struct {
	Email string `json:"email"`
}

type otpVerify struct {
	ChallengeID string `json:"challenge_id"`
	Email       string `json:"email"`
	Code        string `json:"code"`
	KeyName     string `json:"key_name"`
}

type keyRequest struct {
	Name      string `json:"name"`
	AccountID string `json:"account_id"`
}

type limiterEntry struct {
	limiter *rate.Limiter
	seen    time.Time
	limits  usage.Limits
}

type limiterSet struct {
	mu        sync.Mutex
	items     map[string]limiterEntry
	max       int
	lastSweep time.Time
}

func newLimiterSet() *limiterSet {
	return &limiterSet{items: make(map[string]limiterEntry), max: 10000}
}

func (s *limiterSet) allow(key string, l usage.Limits) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.lastSweep) >= time.Minute {
		for k, v := range s.items {
			if now.Sub(v.seen) > 10*time.Minute {
				delete(s.items, k)
			}
		}
		s.lastSweep = now
	}
	if v, ok := s.items[key]; ok {
		v.seen = now
		if v.limits != l {
			v.limiter.SetLimit(rate.Limit(l.RequestsPerSecond))
			v.limiter.SetBurst(l.Burst)
			v.limits = l
		}
		s.items[key] = v
		return v.limiter.Allow()
	}
	if len(s.items) >= s.max {
		for k := range s.items {
			delete(s.items, k)
			break
		}
	}
	x := rate.NewLimiter(rate.Limit(l.RequestsPerSecond), l.Burst)
	s.items[key] = limiterEntry{x, now, l}
	return x.Allow()
}

func (s *limiterSet) remaining(key string, l usage.Limits) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.items[key]; ok {
		n := int(v.limiter.Tokens())
		if n < 0 {
			return 0
		}
		return n
	}
	return l.Burst
}
