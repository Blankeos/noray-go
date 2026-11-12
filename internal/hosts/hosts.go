// Package hosts holds registered hosts (HostEntity / HostRepository).
//
// Every party, host or client, registers as a "host" with register-host and
// gets an open id (OID, shared with others) and a private id (PID, only used
// to register its public UDP address with the registrar).
package hosts

import (
	"net/netip"
	"sync"

	"github.com/blankeos/noray-go/internal/repository"
)

// Host is a registered party.
type Host struct {
	OID   string
	PID   string
	Owner any // the TCP connection that registered it

	rinfo netip.AddrPort // public UDP address, once registered
	relay int
}

// Repository is a concurrency-safe host store with O(1) lookups by oid, pid
// and owning connection (the original scanned every host for the last two).
type Repository struct {
	mu      sync.RWMutex
	repo    *repository.Repository[string, *Host]
	byPID   map[string]*Host
	byOwner map[any]*Host // first host registered by the connection
}

// NewRepository creates an empty repository.
func NewRepository() *Repository {
	return &Repository{
		repo:    repository.New[string, *Host](func(h *Host) string { return h.OID }, nil),
		byPID:   map[string]*Host{},
		byOwner: map[any]*Host{},
	}
}

// Add stores h; repository.ErrIDInUse if the oid is taken.
func (r *Repository) Add(h *Host) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.repo.Add(h); err != nil {
		return err
	}
	if _, ok := r.byPID[h.PID]; !ok {
		r.byPID[h.PID] = h
	}
	if h.Owner != nil {
		if _, ok := r.byOwner[h.Owner]; !ok {
			r.byOwner[h.Owner] = h
		}
	}
	return nil
}

// Remove deletes h; false if it was not stored.
func (r *Repository) Remove(h *Host) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.repo.Find(h.OID)
	if !ok || cur != h {
		return false
	}
	r.repo.Remove(h.OID)
	if r.byPID[h.PID] == h {
		delete(r.byPID, h.PID)
	}
	if h.Owner != nil && r.byOwner[h.Owner] == h {
		delete(r.byOwner, h.Owner)
	}
	return true
}

// Has reports whether an oid is registered.
func (r *Repository) Has(oid string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.repo.Has(oid)
}

// Len is the number of hosts.
func (r *Repository) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.repo.Len()
}

// Snapshot is a copy of a host's mutable state.
type Snapshot struct {
	Host    *Host
	Remote  netip.AddrPort
	HasAddr bool
}

func snap(h *Host) (Snapshot, bool) {
	if h == nil {
		return Snapshot{}, false
	}
	return Snapshot{Host: h, Remote: h.rinfo, HasAddr: h.rinfo.IsValid()}, true
}

// Find looks up by oid.
func (r *Repository) Find(oid string) (Snapshot, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, _ := r.repo.Find(oid)
	return snap(h)
}

// FindByPID looks up by pid.
func (r *Repository) FindByPID(pid string) (Snapshot, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return snap(r.byPID[pid])
}

// FindByOwner returns the first host registered by a connection.
func (r *Repository) FindByOwner(owner any) (Snapshot, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return snap(r.byOwner[owner])
}

// RegisterRemote implements relay.RemoteRegistry: the first registered
// address sticks, later ones are "repeats" and do not change it.
func (r *Repository) RegisterRemote(pid string, addr netip.AddrPort) (repeat, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.byPID[pid]
	if h == nil {
		return false, false
	}
	if h.rinfo.IsValid() {
		return true, true
	}
	h.rinfo = addr
	return false, true
}

// SetRelay records the relay port assigned to a host.
func (r *Repository) SetRelay(h *Host, port int) {
	r.mu.Lock()
	h.relay = port
	r.mu.Unlock()
}
