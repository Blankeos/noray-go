package config

import "testing"

func envOf(m map[string]string) Env {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestWaitForPeer(t *testing.T) {
	c, err := FromEnv(envOf(nil))
	if err != nil || c.UDPRelay.WaitForPeer {
		t.Fatalf("default should be off (%v)", err)
	}
	c, err = FromEnv(envOf(map[string]string{"NORAY_UDP_RELAY_WAIT_FOR_PEER": "true"}))
	if err != nil || !c.UDPRelay.WaitForPeer {
		t.Fatalf("NORAY_UDP_RELAY_WAIT_FOR_PEER=true should turn it on (%v)", err)
	}
}
