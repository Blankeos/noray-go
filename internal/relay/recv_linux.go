package relay

import (
	"net/netip"
	"syscall"
	"unsafe"
)

// recvState holds the sockaddr buffer for one socket's reader, so receiving
// never allocates.
type recvState struct {
	rsa syscall.RawSockaddrAny
	len uint32
}

// recvFrom is a zero-allocation non-blocking recvfrom(2).
func recvFrom(fd int, b []byte, st *recvState) (int, netip.AddrPort, error) {
	st.len = syscall.SizeofSockaddrAny
	n, _, e := syscall.RawSyscall6(syscall.SYS_RECVFROM, uintptr(fd),
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0,
		uintptr(unsafe.Pointer(&st.rsa)), uintptr(unsafe.Pointer(&st.len)))
	if e != 0 {
		return 0, netip.AddrPort{}, e
	}
	var from netip.AddrPort
	if st.rsa.Addr.Family == syscall.AF_INET {
		sa := (*syscall.RawSockaddrInet4)(unsafe.Pointer(&st.rsa))
		p := (*[2]byte)(unsafe.Pointer(&sa.Port))
		from = netip.AddrPortFrom(netip.AddrFrom4(sa.Addr), uint16(p[0])<<8|uint16(p[1]))
	}
	return int(n), from, nil
}
