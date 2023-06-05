package netemlite

//
// Network layer simulation
//

import (
	"net/netip"
	"sync"
	"syscall"
)

// Network simulates a TCP/IP network. The zero value is
// invalid; please, use [NewNetwork] to construct.
type Network struct {
	// closed is closed by Close to terminate the Network layer.
	closed chan any

	// deleteConnUDP receives requests to delete UDP conns.
	deleteConnUDP chan *networkDeleteConnUDP

	// newConnUDP receives requests to track UDP conns.
	newConnUDP chan *networkNewConnUDP

	// once ensures that Close has "once" semantics.
	once sync.Once

	// readUDP receives requests to read UDP datagrams.
	readUDP chan *networkReadUDP

	// udp tracks all the currently open UDP conns. This field is
	// EXCLUSIVELY MUTATED by the background worker goroutine.
	udp map[netip.AddrPort]*networkConnStateUDP

	// writeUDP receives requests to write UDP datagrams.
	writeUDP chan *networkWriteUDP
}

// NewNetwork constructs an [Network] instance and spawns a background goroutine
// for processing network events that runs until you call the Close method.
func NewNetwork() *Network {
	n := &Network{
		closed:        make(chan any),
		deleteConnUDP: make(chan *networkDeleteConnUDP),
		newConnUDP:    make(chan *networkNewConnUDP),
		once:          sync.Once{},
		readUDP:       make(chan *networkReadUDP),
		udp:           map[netip.AddrPort]*networkConnStateUDP{},
		writeUDP:      make(chan *networkWriteUDP),
	}
	go n.loop()
	return n
}

// Close shuts down the background goroutine.
func (n *Network) Close() error {
	n.once.Do(func() {
		close(n.closed)
	})
	return nil
}

// networkConnStateUDP contains the state of an UDP connection.
type networkConnStateUDP struct {
	// blockedReads contains the blocked reads.
	blockedReads []*networkReadUDP

	// blockedWrites contains the blocked writes.
	blockedWrites []*networkWriteUDP
}

// networkNewConnUDP is a request to track a UDP conn.
type networkNewConnUDP struct {
	// ack is closed by the Network layer to acknowledge that
	// it has processed this message.
	ack chan any

	// err is the error set by the Network layer.
	err error

	// localAddr is the UDP conn address.
	localAddr netip.AddrPort
}

// networkDeleteConnUDP is a request to delete a UDP conn.
type networkDeleteConnUDP struct {
	// ack is closed by the Network layer to acknowledge that
	// it has processed this message.
	ack chan any

	// err is the error set by the Network layer.
	err error

	// localAddr is the UDP conn address.
	localAddr netip.AddrPort
}

// networkWriteUDP is a request to write a datagram.
type networkWriteUDP struct {
	// ack is closed by the Network layer to acknowledge that
	// it has processed this message.
	ack chan any

	// destAddr is the destination address of the datagram.
	destAddr netip.AddrPort

	// err is the error set by the Network layer.
	err error

	// payload is the datagram payload.
	payload []byte

	// sourceAddr is the source address of the datagram.
	sourceAddr netip.AddrPort
}

// networkReadUDP is a request to read a datagram.
type networkReadUDP struct {
	// ack is closed by the Network layer to acknowledge that
	// it has processed this message.
	ack chan any

	// buffer is the buffer to contain the payload, written by the Network layer.
	buffer []byte

	// count is the number of bytes written, set by the Network layer.
	count int

	// err is the error set by the Network layer.
	err error

	// localAddr is the local address of the Socket.
	localAddr netip.AddrPort

	// senderAddr is the sender address set by the Network layer.
	senderAddr netip.AddrPort
}

// loop is the network main loop.
func (n *Network) loop() {
	for {
		select {
		case <-n.closed:
			return

		case req := <-n.newConnUDP:
			n.onNewConnUDP(req)

		case req := <-n.readUDP:
			n.onReadUDP(req)

		case req := <-n.writeUDP:
			n.onWriteUDP(req)

		case req := <-n.deleteConnUDP:
			n.onDeleteConnUDP(req)
		}
	}
}

// onNewConnUDP handles a request to track a new UDP conn.
func (n *Network) onNewConnUDP(req *networkNewConnUDP) {
	// always acknowledge the caller
	defer close(req.ack)

	// make sure there is no existing state
	if n.udp[req.localAddr] != nil {
		req.err = syscall.EADDRNOTAVAIL
		return
	}

	// track the new UDP conn
	n.udp[req.localAddr] = &networkConnStateUDP{
		blockedReads:  []*networkReadUDP{},
		blockedWrites: []*networkWriteUDP{},
	}
}

// onReadUDP handles a request to read an UDP datagram.
func (n *Network) onReadUDP(read *networkReadUDP) {
	// get the source socket
	source := n.udp[read.localAddr]

	// if the source does not exist, this is clearly a bug
	if source == nil {
		read.err = syscall.EBADF
		close(read.ack)
		return
	}

	// if there are no blocked weites, block this read
	if len(source.blockedWrites) <= 0 {
		source.blockedReads = append(source.blockedReads, read)
		return
	}

	// get the first blocked write
	write := source.blockedWrites[0]
	source.blockedWrites = append([]*networkWriteUDP{}, source.blockedWrites[1:]...)

	// invoke common algorithm for readwrite
	n.finishReadWrite(read, write)
}

// onWriteUDP handles a request to write an UDP datagram.
func (n *Network) onWriteUDP(write *networkWriteUDP) {
	// get the destination socket
	dest := n.udp[write.destAddr]

	// if the dest does not exist, silently drop the datagram.
	if dest == nil {
		close(write.ack)
		return
	}

	// if there are no blocked reads, block this write.
	if len(dest.blockedReads) <= 0 {
		dest.blockedWrites = append(dest.blockedWrites, write)
		return
	}

	// get the first blocked read
	read := dest.blockedReads[0]
	dest.blockedReads = append([]*networkReadUDP{}, dest.blockedReads[1:]...)

	// invoke common algorithm for readwrite
	n.finishReadWrite(read, write)
}

// finishReadWrite finishes a read and a write.
func (n *Network) finishReadWrite(read *networkReadUDP, write *networkWriteUDP) {
	// copy bytes from the writer to the reader
	read.count = copy(read.buffer, write.payload)

	// unblock the writer
	close(write.ack)

	// take note of the sender
	read.senderAddr = write.sourceAddr

	// unblock the reader
	close(read.ack)
}

// onDeleteConnUDP handles a request to forget an existing UDP conn.
func (n *Network) onDeleteConnUDP(req *networkDeleteConnUDP) {
	// always acknowledge the caller
	defer close(req.ack)

	// fail if the destination is not available
	if n.udp[req.localAddr] == nil {
		req.err = syscall.EBADF
		return
	}

	// forget the existing UDP conn
	delete(n.udp, req.localAddr)
}
