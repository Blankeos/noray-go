package server

import (
	"errors"
	"strconv"

	"github.com/blankeos/noray-go/internal/hosts"
	"github.com/blankeos/noray-go/internal/logger"
	"github.com/blankeos/noray-go/internal/repository"
	"github.com/blankeos/noray-go/internal/trimsock"
)

// writer sends bytes to a client connection without blocking for long.
type writer interface {
	write(b []byte) error
}

// session is the protocol state of one TCP client, independent of how the
// connection is served (goroutine per connection, or the Linux epoll loop).
type session struct {
	s      *Server
	out    writer
	remote string
	reader trimsock.Reader
	hosts  []*hosts.Host
}

func (c *session) write(b []byte) error { return c.out.write(b) }

// ingest feeds received bytes and runs every complete command.
func (c *session) ingest(data []byte) {
	if !c.reader.Ingest(data) {
		c.s.log.Warn(logger.F{"remote": c.remote}, "Command buffer overflow, discarding buffered data")
	}
	for {
		cmd, ok := c.reader.Next()
		if !ok {
			break
		}
		c.handle(cmd)
	}
	c.reader.Release()
}

// closed removes the hosts registered by this connection.
func (c *session) closed() {
	s := c.s
	log := s.log.Child("cmd:register-host")
	for _, h := range c.hosts {
		log.Info(logger.F{"oid": h.OID, "pid": h.PID}, "Host disconnected, removing from repository")
		if s.Hosts.Remove(h) {
			s.activeHosts.Dec()
		}
	}
	c.hosts = nil
}

// commandError is a handler failure, answered like the original's reactor
// error handler.
type commandError struct{ msg string }

func (e commandError) Error() string { return e.msg }

func fail(msg string) error { return commandError{msg} }

func (c *session) handle(cmd *trimsock.Command) {
	// Responses and stream ends never open an exchange; the original looked
	// up an exchange for them and crashed the process (unhandled rejection)
	// when there was none. Noray never sends requests, so just ignore them.
	if cmd.IsSuccessResponse || cmd.IsErrorResponse || cmd.IsStreamEnd {
		return
	}
	var err error
	switch cmd.Name {
	case "register-host":
		err = c.registerHost()
	case "connect":
		err = c.connect(cmd)
	case "connect-relay":
		err = c.connectRelay(cmd)
	default:
		return // unknown commands are ignored
	}
	if err == nil {
		return
	}
	// failOrSend({ name, data }): the data is dropped by the serializer, so
	// the client gets just "<name>\n", or "!<id>\n" for request commands.
	var out []byte
	if id, ok := cmd.ID(); ok {
		out = trimsock.AppendErrorResponse(nil, id)
	} else {
		out = trimsock.AppendCommand(nil, cmd.Name)
	}
	if c.s.log.Enabled(logger.Debug) {
		c.s.log.Debug(logger.F{"command": cmd.Name, "err": err.Error()}, "Command failed")
	}
	c.write(out)
}

func (c *session) registerHost() error {
	s := c.s
	h := &hosts.Host{PID: s.genPID(), Owner: c}
	var err error
	for attempt := 0; attempt < 16; attempt++ {
		h.OID = s.genOID()
		if err = s.Hosts.Add(h); !errors.Is(err, repository.ErrIDInUse) {
			break
		}
	}
	if err != nil {
		return err
	}
	s.activeHosts.Inc()
	c.hosts = append(c.hosts, h)
	out := trimsock.AppendCommand(nil, "set-oid", h.OID)
	out = trimsock.AppendCommand(out, "set-pid", h.PID)
	c.write(out)
	s.log.Child("cmd:register-host").Info(logger.F{"oid": h.OID, "pid": h.PID},
		"Registered host from address %s", c.remote)
	return nil
}

func requireText(cmd *trimsock.Command) (string, error) {
	if !cmd.HasText {
		return "", fail("Command has no text data!")
	}
	return cmd.Text, nil
}

func (c *session) connect(cmd *trimsock.Command) error {
	oid, err := requireText(cmd)
	if err != nil {
		return err
	}
	s := c.s
	host, ok := s.Hosts.Find(oid)
	if !ok {
		return fail("Unknown host oid: " + oid)
	}
	if !host.HasAddr {
		return fail("Host has no remote info registered!")
	}
	client, ok := s.Hosts.FindByOwner(c)
	if !ok {
		return fail("Unknown client from address")
	}
	if !client.HasAddr {
		return fail("Client has no remote info registered!")
	}
	hostAddr := host.Remote.String()
	clientAddr := client.Remote.String()
	c.write(trimsock.AppendCommand(nil, "connect", hostAddr))
	host.Host.Owner.(*session).write(trimsock.AppendCommand(nil, "connect", clientAddr))
	if s.log.Enabled(logger.Debug) {
		s.log.Child("cmd:connect").Debug(logger.F{"client": clientAddr, "host": hostAddr, "oid": oid}, "Connected client to host")
	}
	return nil
}

func (c *session) connectRelay(cmd *trimsock.Command) error {
	oid, err := requireText(cmd)
	if err != nil {
		return err
	}
	s := c.s
	host, ok := s.Hosts.Find(oid)
	if !ok {
		return fail("Unknown host oid: " + oid)
	}
	client, ok := s.Hosts.FindByOwner(c)
	if !ok {
		return fail("Unknown client from address")
	}
	// Like the original, the host's relay is ensured before the client's
	// remote address is checked.
	hostRelay, err := c.ensureRelay(host)
	if err != nil {
		return err
	}
	clientRelay, err := c.ensureRelay(client)
	if err != nil {
		return err
	}
	c.write(trimsock.AppendCommand(nil, "connect-relay", strconv.Itoa(hostRelay)))
	host.Host.Owner.(*session).write(trimsock.AppendCommand(nil, "connect-relay", strconv.Itoa(clientRelay)))
	if s.log.Enabled(logger.Debug) {
		s.log.Child("cmd:connect-relay").Debug(logger.F{"client": c.remote, "relay": hostRelay, "oid": oid}, "Connected client to host")
	}
	return nil
}

// ensureRelay returns the relay port for a host's registered address,
// creating the relay if needed (or if the old one expired).
func (c *session) ensureRelay(h hosts.Snapshot) (int, error) {
	if !h.HasAddr {
		// The original crashed reading .address of undefined here (and
		// replied with the generic error).
		return 0, fail("TypeError: Cannot read properties of undefined (reading 'address')")
	}
	e, _, err := c.s.Relays.CreateRelay(h.Remote)
	if err != nil {
		return 0, err
	}
	c.s.Hosts.SetRelay(h.Host, e.Port)
	return e.Port, nil
}
