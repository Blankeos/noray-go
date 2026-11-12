//go:build !linux

package server

func listenTCP(s *Server, addr string) (tcpFrontend, error) { return listenGoroutine(s, addr) }
