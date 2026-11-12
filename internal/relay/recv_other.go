//go:build !linux

package relay

import (
	"net/netip"
	"syscall"
)

type recvState struct{}

// recvFrom is a non-blocking recvfrom(2) (allocates a sockaddr; the
// zero-allocation version is Linux only).
func recvFrom(fd int, b []byte, _ *recvState) (int, netip.AddrPort, error) {
	n, sa, err := syscall.Recvfrom(fd, b, 0)
	if err != nil {
		return 0, netip.AddrPort{}, err
	}
	var from netip.AddrPort
	if sa4, ok := sa.(*syscall.SockaddrInet4); ok {
		from = netip.AddrPortFrom(netip.AddrFrom4(sa4.Addr), uint16(sa4.Port))
	}
	return n, from, nil
}
