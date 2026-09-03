package httpapi

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const maxTrackedClients = 10_000

type clientWindow struct {
	started time.Time
	count   int
}

type clientLimiter struct {
	mu       sync.Mutex
	limit    int
	clients  map[string]clientWindow
	lastTrim time.Time
}

func newClientLimiter(limit int) *clientLimiter {
	return &clientLimiter{limit: limit, clients: make(map[string]clientWindow)}
}

func (l *clientLimiter) allow(client string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	window := l.clients[client]
	if window.started.IsZero() || now.Sub(window.started) >= time.Minute || now.Before(window.started) {
		window = clientWindow{started: now}
	}
	if window.count >= l.limit {
		return false
	}
	window.count++
	l.clients[client] = window
	if len(l.clients) > maxTrackedClients && now.Sub(l.lastTrim) >= time.Minute {
		for key, candidate := range l.clients {
			if now.Sub(candidate.started) >= time.Minute {
				delete(l.clients, key)
			}
		}
		l.lastTrim = now
	}
	return true
}

func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "unknown"
}

type keyedLocks struct {
	mu    sync.Mutex
	items map[string]*keyedLock
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

func (l *keyedLocks) lock(key string) func() {
	l.mu.Lock()
	if l.items == nil {
		l.items = make(map[string]*keyedLock)
	}
	item := l.items[key]
	if item == nil {
		item = &keyedLock{}
		l.items[key] = item
	}
	item.refs++
	l.mu.Unlock()

	item.mu.Lock()
	return func() {
		item.mu.Unlock()
		l.mu.Lock()
		item.refs--
		if item.refs == 0 {
			delete(l.items, key)
		}
		l.mu.Unlock()
	}
}
