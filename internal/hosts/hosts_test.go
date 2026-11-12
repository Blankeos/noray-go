package hosts

import (
	"net/netip"
	"testing"
)

func TestRepository(t *testing.T) {
	r := NewRepository()
	owner := new(int)
	a := &Host{OID: "o1", PID: "p1", Owner: owner}
	b := &Host{OID: "o2", PID: "p2", Owner: owner}
	if r.Add(a) != nil || r.Add(b) != nil || r.Add(&Host{OID: "o1"}) == nil {
		t.Fatal("add")
	}
	if s, ok := r.FindByOwner(owner); !ok || s.Host != a {
		t.Fatal("findBySocket returns the first registered host")
	}
	if s, ok := r.FindByPID("p2"); !ok || s.Host != b || s.HasAddr {
		t.Fatal("findByPid")
	}
	addr := netip.MustParseAddrPort("1.2.3.4:5")
	if rep, ok := r.RegisterRemote("p1", addr); rep || !ok {
		t.Fatal("first registration")
	}
	if rep, ok := r.RegisterRemote("p1", netip.MustParseAddrPort("9.9.9.9:9")); !rep || !ok {
		t.Fatal("repeat")
	}
	if s, _ := r.Find("o1"); s.Remote != addr {
		t.Fatal("first address sticks")
	}
	if _, ok := r.RegisterRemote("nope", addr); ok {
		t.Fatal("unknown pid")
	}
	if !r.Remove(a) || r.Remove(a) || r.Has("o1") || r.Len() != 1 {
		t.Fatal("remove")
	}
	if _, ok := r.FindByOwner(owner); ok {
		t.Fatal("owner index cleared")
	}
}
