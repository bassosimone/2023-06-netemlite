package netemlite

//
// UDP conn
//

import (
	"net"
	"net/netip"
	"os"
	"sync"
	"syscall"
	"time"
)

// UDPConn is an UDP connection. The zero value of this struct
// is invalid; please, use [NewUDPConn] to construct.
type UDPConn struct {
	// closed is closed by Closed.
	closed chan any

	// localAddr is the READONLY local address.
	localAddr netip.AddrPort

	// network is the READONLY network to use.
	network *Network

	// once ensures Close runs just once.
	once sync.Once

	// peerAddr is the READONLY, OPTIONAL peer address.
	peerAddr netip.AddrPort

	// readDeadline contains the read deadline.
	readDeadline *pipeDeadline

	// writeDeadline contains the write deadline.
	writeDeadline *pipeDeadline
}

// A UDPConn is also a valid net.PacketConn.
var _ net.PacketConn = &UDPConn{}

// A UDPConn is also a valid net.Conn.
var _ net.Conn = &UDPConn{}

// NewUDPConn creates a new [UDPConn] instance.
//
// Arguments:
//
// - network is the [Network] to use;
//
// - localAddr is the local address of the connection;
//
// - peerAddr is the OPTIONAL peer address.
//
// A zero value remoteAddr implies this socket is not connected.
func NewUDPConn(network *Network, localAddr, peerAddr netip.AddrPort) (*UDPConn, error) {
	// initialize the connection
	c := &UDPConn{
		closed:        make(chan any),
		localAddr:     localAddr,
		network:       network,
		once:          sync.Once{},
		peerAddr:      peerAddr,
		readDeadline:  makePipeDeadline(),
		writeDeadline: makePipeDeadline(),
	}

	// initialize the request to register the connection
	req := &networkNewConnUDP{
		ack:       make(chan any),
		err:       nil,
		localAddr: localAddr,
	}

	// attempt to register the connection
	select {
	case <-network.closed:
		return nil, net.ErrClosed

	case network.newConnUDP <- req:
		select {
		case <-network.closed:
			return nil, net.ErrClosed

		case <-req.ack:
			return c, req.err
		}
	}
}

// LocalAddr returns the local addr.
func (c *UDPConn) LocalAddr() net.Addr {
	return net.UDPAddrFromAddrPort(c.localAddr)
}

// RemoteAddr returns the POSSIBLY NIL remote addr.
func (c *UDPConn) RemoteAddr() net.Addr {
	if !c.peerAddr.IsValid() {
		return nil
	}
	return net.UDPAddrFromAddrPort(c.peerAddr)
}

// SetReadDeadline sets the read deadline.
func (c *UDPConn) SetReadDeadline(t time.Time) error {
	select {
	case <-c.closed:
		return net.ErrClosed

	default:
		c.readDeadline.set(t)
		return nil
	}
}

// SetWriteDeadline sets the read deadline.
func (c *UDPConn) SetWriteDeadline(t time.Time) error {
	select {
	case <-c.closed:
		return net.ErrClosed

	default:
		c.writeDeadline.set(t)
		return nil
	}
}

// SetDeadline sets the read and the write deadlines.
func (c *UDPConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

// Close closes the connection.
func (c *UDPConn) Close() error {
	c.once.Do(func() {
		// create request for shutting down
		req := &networkDeleteConnUDP{
			ack:       make(chan any),
			err:       nil,
			localAddr: c.localAddr,
		}

		// tell the network we're shutting down
		select {
		case <-c.network.closed:
			// nothing

		case c.network.deleteConnUDP <- req:
			select {
			case <-c.network.closed:
				// nothing

			case <-req.ack:
				// nothing
			}
		}

		// interrupt all pending I/O
		close(c.closed)
	})
	return nil
}

// Read reads from a connected UDP socket.
func (c *UDPConn) Read(buffer []byte) (int, error) {
	// make sure we're connected
	if !c.peerAddr.IsValid() {
		return 0, syscall.ENOTCONN
	}

	for {
		// read a datagram from the network
		count, source, err := c.commonRead(buffer)

		// stop in case of errors
		if err != nil {
			return 0, err
		}

		// make sure the datagram is from the peer
		if source != c.peerAddr {
			continue
		}

		// successfull return to the caller
		return count, nil
	}
}

// ReadFrom reads from a non-connected UDP socket.
func (c *UDPConn) ReadFrom(buffer []byte) (int, net.Addr, error) {
	// make sure we're not connected
	if c.peerAddr.IsValid() {
		return 0, nil, syscall.EISCONN
	}

	// read from the network
	count, source, err := c.commonRead(buffer)

	// handle errors
	if err != nil {
		return 0, nil, err
	}

	// handle successful case
	return count, net.UDPAddrFromAddrPort(source), nil
}

// commonRead is the common code for reading
func (c *UDPConn) commonRead(buffer []byte) (int, netip.AddrPort, error) {
	// prepare request
	req := &networkReadUDP{
		ack:        make(chan any),
		buffer:     buffer,
		count:      0,
		err:        nil,
		localAddr:  c.localAddr,
		senderAddr: netip.AddrPort{},
	}

	// issue the request
	select {
	case <-c.closed:
		return 0, netip.AddrPort{}, net.ErrClosed

	case <-c.network.closed:
		return 0, netip.AddrPort{}, net.ErrClosed

	case <-c.readDeadline.wait():
		return 0, netip.AddrPort{}, os.ErrDeadlineExceeded

	case c.network.readUDP <- req:

		// receive ack
		select {
		case <-c.closed:
			return 0, netip.AddrPort{}, net.ErrClosed

		case <-c.network.closed:
			return 0, netip.AddrPort{}, net.ErrClosed

		case <-c.readDeadline.wait():
			return 0, netip.AddrPort{}, os.ErrDeadlineExceeded

		case <-req.ack:
			return req.count, req.senderAddr, req.err
		}
	}
}

// Write writes data on a connected UDP socket.
func (c *UDPConn) Write(data []byte) (int, error) {
	// make sure we not connected
	if !c.peerAddr.IsValid() {
		return 0, syscall.ENOTCONN
	}

	// use common write code
	return c.commonWrite(data, c.peerAddr)
}

// WriteTo writes data on an unconnected UDP socket.
func (c *UDPConn) WriteTo(data []byte, addr net.Addr) (int, error) {
	// make sure we're not connected
	if c.peerAddr.IsValid() {
		return 0, syscall.EISCONN
	}

	// parse the destination address
	destAddr, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return 0, syscall.EINVAL
	}

	// use common write code
	return c.commonWrite(data, destAddr)
}

// commonWrite is the common code for writing.
func (c *UDPConn) commonWrite(data []byte, destAddr netip.AddrPort) (int, error) {
	// prepare request
	req := &networkWriteUDP{
		ack:        make(chan any),
		destAddr:   destAddr,
		payload:    data,
		sourceAddr: c.localAddr,
	}

	// issue the request
	select {
	case <-c.closed:
		return 0, net.ErrClosed

	case <-c.network.closed:
		return 0, net.ErrClosed

	case <-c.readDeadline.wait():
		return 0, os.ErrDeadlineExceeded

	case c.network.writeUDP <- req:

		// receive ack
		select {
		case <-c.closed:
			return 0, net.ErrClosed

		case <-c.network.closed:
			return 0, net.ErrClosed

		case <-c.readDeadline.wait():
			return 0, os.ErrDeadlineExceeded

		case <-req.ack:
			return len(data), req.err
		}
	}
}
