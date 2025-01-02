// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package metalbond

import (
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

var RetryIntervalMin = 5
var RetryIntervalMax = 5

type metalBondPeer struct {
	conn       *net.Conn
	id         string
	remoteAddr string
	localIP    string
	localAddr  string
	direction  ConnectionDirection
	isServer   bool

	mtxReset sync.RWMutex
	mtxState sync.RWMutex
	state    ConnectionState

	receivedRoutes    routeTable
	subscribedVNIs    map[VNI]bool
	mtxSubscribedVNIs sync.RWMutex

	metalbond *MetalBond

	keepaliveInterval uint32
	keepaliveTimer    *time.Timer

	shutdown      chan bool
	keepaliveStop chan bool
	txChan        chan []byte
	txChanClose   chan bool
	rxHello       chan msgHello
	rxKeepalive   chan msgKeepalive
	rxSubscribe   chan msgSubscribe
	rxUnsubscribe chan msgUnsubscribe
	rxUpdate      chan msgUpdate
	wg            sync.WaitGroup

	txChanCapacity           int
	rxChanEventCapacity      int
	rxChanDataUpdateCapacity int

	maxTxChanDepth               int
	maxRxChanHelloMaxDepth       int
	maxRxChanKeepaliveMaxDepth   int
	maxRxChanSubscribeMaxDepth   int
	maxRxChanUnsubscribeMaxDepth int
	maxRxChanUpdateMaxDepth      int

	// only used for unit test
	stopReceive           bool
	lastKeepaliveSent     time.Time
	lastKeepaliveReceived time.Time
	manuallyRemoved       bool
}

func newMetalBondPeer(pconn *net.Conn, remoteAddr string, localIP string, txChanCapacity int, rxChanEventCapacity int, rxChanDataUpdateCapacity int, keepaliveInterval uint32, direction ConnectionDirection, metalbond *MetalBond) *metalBondPeer {

	id := uuid.New()
	peer := &metalBondPeer{
		conn:                     pconn,
		id:                       id.String(),
		remoteAddr:               remoteAddr,
		localIP:                  localIP,
		direction:                direction,
		state:                    CONNECTING,
		receivedRoutes:           newRouteTable(),
		subscribedVNIs:           make(map[VNI]bool),
		keepaliveInterval:        keepaliveInterval,
		txChanCapacity:           txChanCapacity,
		rxChanEventCapacity:      rxChanEventCapacity,
		rxChanDataUpdateCapacity: rxChanDataUpdateCapacity,
		metalbond:                metalbond,
	}

	go peer.handle()

	return peer
}

func (p *metalBondPeer) String() string {
	return p.remoteAddr
}

func (p *metalBondPeer) GetState() ConnectionState {
	p.mtxState.RLock()
	state := p.state
	p.mtxState.RUnlock()
	return state
}

func (p *metalBondPeer) GetLastKeepaliveSent() time.Time {
	return p.lastKeepaliveSent
}

func (p *metalBondPeer) GetLastKeepaliveReceived() time.Time {
	return p.lastKeepaliveReceived
}

func (p *metalBondPeer) Subscribe(vni VNI) error {
	p.log().Debugf("Subscribe to vni %d", vni)

	if p.direction == INCOMING {
		return fmt.Errorf("Cannot subscribe on incoming connection")
	}
	if p.GetState() != ESTABLISHED {
		return fmt.Errorf("Connection not ESTABLISHED")
	}

	msg := msgSubscribe{
		VNI: vni,
	}

	return p.sendMessage(msg)
}

func (p *metalBondPeer) Unsubscribe(vni VNI) error {
	p.log().Debugf("Unsubscribe from vni %d", vni)

	if p.direction == INCOMING {
		return fmt.Errorf("Cannot unsubscribe on incoming connection")
	}

	msg := msgUnsubscribe{
		VNI: vni,
	}

	for dest, nhs := range p.receivedRoutes.GetDestinationsByVNI(vni) {
		for _, nh := range nhs {
			err, _ := p.receivedRoutes.RemoveNextHop(vni, dest, nh, p)
			if err != nil {
				p.log().Errorf("Could not remove received route from peer's receivedRoutes Table: %v", err)
			}
		}
	}

	return p.sendMessage(msg)
}

func (p *metalBondPeer) SendUpdate(upd msgUpdate) error {
	if p.GetState() != ESTABLISHED {
		return fmt.Errorf("Connection not ESTABLISHED")
	}

	if err := p.sendMessage(upd); err != nil {
		p.log().Errorf("Cannot send message: %v", err)
		return err
	}
	return nil
}

// WaitTimeout waits for the peer's goroutines to exit,
// but returns an error if that takes longer than "timeout".
func (p *metalBondPeer) WaitTimeout(timeout time.Duration) error {
	doneCh := make(chan struct{})

	// Run p.wg.Wait() in a separate goroutine:
	go func() {
		p.wg.Wait()
		close(doneCh)
	}()

	// Wait for doneCh or time out:
	select {
	case <-doneCh:
		return nil // success: all goroutines have exited
	case <-time.After(timeout):
		return fmt.Errorf("WaitTimeout exceeded %v, goroutines still not exited", timeout)
	}
}

///////////////////////////////////////////////////////////////////
//            PRIVATE METHODS BELOW                              //
///////////////////////////////////////////////////////////////////

func (p *metalBondPeer) setState(newState ConnectionState) {
	oldState := p.state

	p.mtxState.Lock()
	p.state = newState
	p.mtxState.Unlock()

	if oldState != newState && newState == ESTABLISHED {
		p.metalbond.mtxMySubscriptions.RLock()
		for sub := range p.metalbond.mySubscriptions {
			p.log().Debugf("Replay subscription to VNI %d", sub)
			if err := p.Subscribe(sub); err != nil {
				p.log().Errorf("Cannot subscribe: %v", err)
			}
		}
		p.metalbond.mtxMySubscriptions.RUnlock()

		rt := p.metalbond.getMyAnnouncements()
		for _, vni := range rt.GetVNIs() {
			for dest, hops := range rt.GetDestinationsByVNI(vni) {
				for _, hop := range hops {

					upd := msgUpdate{
						VNI:         vni,
						Destination: dest,
						NextHop:     hop,
					}

					p.log().Debugf(
						"Replaying route to VNI %d, dest %s, next hop %s",
						vni,
						dest.String(),
						hop.String())
					err := p.SendUpdate(upd)
					if err != nil {
						p.log().Errorf("Could not send update to peer: %v", err)
					}
				}
			}
		}
	}

	// Connection lost
	if oldState != newState && newState != ESTABLISHED {
		subscribers := make(map[VNI]map[*metalBondPeer]bool)
		p.metalbond.mtxSubscribers.RLock()
		for vni, peers := range p.metalbond.subscribers {
			for peer := range peers {
				if p == peer {
					if _, ok := subscribers[vni]; !ok {
						subscribers[vni] = make(map[*metalBondPeer]bool)
					}
					subscribers[vni][p] = true
				}
			}
		}
		p.metalbond.mtxSubscribers.RUnlock()

		for vni, peers := range subscribers {
			for peer := range peers {
				if p == peer {
					if err := p.metalbond.removeSubscriber(p, vni); err != nil {
						p.log().Errorf("Cannot remove subscriber: %v", err)
					}
				}
			}
		}
	}
}

func (p *metalBondPeer) log() *logrus.Entry {
	return logrus.WithField("peer", p.remoteAddr).WithField("state", p.GetState().String()).WithField("localAddr", p.localAddr)
}

func (p *metalBondPeer) cleanup() {
	p.log().Debugf("cleanup")

	// unsubscribe from VNIs
	p.mtxSubscribedVNIs.Lock()
	defer p.mtxSubscribedVNIs.Unlock()

	for vni := range p.subscribedVNIs {
		err := p.metalbond.Unsubscribe(vni)
		if err != nil {
			p.log().Errorf("Could not unsubscribe from VNI: %v", err)
		}
	}

	// remove received routes from this peer from metalbond database
	p.log().Info("Removing all received nexthops from peer")
	for _, vni := range p.receivedRoutes.GetVNIs() {
		for dest, nhs := range p.receivedRoutes.GetDestinationsByVNI(vni) {
			for _, nh := range nhs {
				err, _ := p.receivedRoutes.RemoveNextHop(vni, dest, nh, p)
				if err != nil {
					p.log().Errorf("Could not remove received route from peer's receivedRoutes Table: %v", err)
					return
				}

				if err := p.metalbond.removeReceivedRoute(p, vni, dest, nh); err != nil {
					p.log().Errorf("Cannot remove received route from metalbond db: %v", err)
				}
			}
		}
	}

	peerID := p.remoteAddr
	localID := p.id
	// Remove metrics associated with this peer
	metricTxChanDepth.DeleteLabelValues(localID, peerID)
	metricTxChanMaxDepth.DeleteLabelValues(localID, peerID)
	metricRxChanHelloMaxDepth.DeleteLabelValues(localID, peerID)
	metricRxChanKeepaliveMaxDepth.DeleteLabelValues(localID, peerID)
	metricRxChanSubscribeMaxDepth.DeleteLabelValues(localID, peerID)
	metricRxChanUnsubscribeMaxDepth.DeleteLabelValues(localID, peerID)
	metricRxChanUpdateMaxDepth.DeleteLabelValues(localID, peerID)
	metricSubscriptionCount.DeleteLabelValues(localID, peerID)
}

func (p *metalBondPeer) handle() {
	p.wg.Add(1)
	defer p.wg.Done()

	p.txChan = make(chan []byte, p.txChanCapacity)
	p.shutdown = make(chan bool, 5)
	p.keepaliveStop = make(chan bool, 5)
	p.txChanClose = make(chan bool, 5)
	p.rxHello = make(chan msgHello, p.rxChanEventCapacity)
	p.rxKeepalive = make(chan msgKeepalive, p.rxChanEventCapacity)
	p.rxSubscribe = make(chan msgSubscribe, p.rxChanDataUpdateCapacity)
	p.rxUnsubscribe = make(chan msgUnsubscribe, p.rxChanDataUpdateCapacity)
	p.rxUpdate = make(chan msgUpdate, p.rxChanDataUpdateCapacity)

	// outgoing connections still need to be established. pconn is nil.
	for p.conn == nil {

		select {
		case <-p.shutdown:
			p.cleanup()

			// exit handle() thread
			return
		default:
			// proceed
		}

		var err error
		localAddr := &net.TCPAddr{}
		if p.localIP != "" {
			localAddr, err = net.ResolveTCPAddr("tcp", p.localIP+":0")
			if err != nil {
				p.log().Errorf("Error resolving local address: %s", err)
				return
			}
		}

		remoteAddr, err := net.ResolveTCPAddr("tcp", p.remoteAddr)
		if err != nil {
			p.log().Errorf("Error resolving remove address: %s", err)
			return
		}

		tcpConn, err := net.DialTCP("tcp", localAddr, remoteAddr)
		if err != nil {
			retry := time.Duration(rand.Intn(RetryIntervalMax)+RetryIntervalMin) * time.Second
			logrus.Infof("Cannot connect to server - %v - retry in %v", err, retry)
			time.Sleep(retry)
			continue
		}

		conn := net.Conn(tcpConn)
		p.localAddr = conn.LocalAddr().String()
		p.conn = &conn

	}

	go p.rxLoop()
	go p.txLoop()

	if p.direction == OUTGOING {
		helloMsg := msgHello{
			KeepaliveInterval: p.keepaliveInterval,
		}

		if err := p.sendMessage(helloMsg); err != nil {
			p.log().Errorf("Failed to send message: %v", err)
		}
		p.setState(HELLO_SENT)
	}

	for {
		select {
		case msg := <-p.rxHello:
			p.log().Debugf("Received HELLO message")
			// Track depth of rxHello channel for monitoring
			currentDepth := len(p.rxHello)
			if currentDepth > p.maxRxChanHelloMaxDepth {
				p.maxRxChanHelloMaxDepth = currentDepth
				metricRxChanHelloMaxDepth.WithLabelValues(p.id, p.remoteAddr).Set(float64(p.maxRxChanHelloMaxDepth))
			}
			p.processRxHello(msg)

		case msg := <-p.rxKeepalive:
			p.log().Tracef("Received KEEPALIVE message")
			// Track depth of rxKeepalive channel for monitoring
			currentDepth := len(p.rxKeepalive)
			if currentDepth > p.maxRxChanKeepaliveMaxDepth {
				p.maxRxChanKeepaliveMaxDepth = currentDepth
				metricRxChanKeepaliveMaxDepth.WithLabelValues(p.id, p.remoteAddr).Set(float64(p.maxRxChanKeepaliveMaxDepth))
			}
			p.processRxKeepalive(msg)

		case msg := <-p.rxSubscribe:
			p.log().Debugf("Received SUBSCRIBE message")
			// Track depth of rxSubscribe channel for monitoring
			currentDepth := len(p.rxSubscribe)
			if currentDepth > p.maxRxChanSubscribeMaxDepth {
				p.maxRxChanSubscribeMaxDepth = currentDepth
				metricRxChanSubscribeMaxDepth.WithLabelValues(p.id, p.remoteAddr).Set(float64(p.maxRxChanSubscribeMaxDepth))
			}
			p.processRxSubscribe(msg)

		case msg := <-p.rxUnsubscribe:
			p.log().Debugf("Received UNSUBSCRIBE message")
			// Track depth of rxUnsubscribe channel for monitoring
			currentDepth := len(p.rxUnsubscribe)
			if currentDepth > p.maxRxChanUnsubscribeMaxDepth {
				p.maxRxChanUnsubscribeMaxDepth = currentDepth
				metricRxChanUnsubscribeMaxDepth.WithLabelValues(p.id, p.remoteAddr).Set(float64(p.maxRxChanUnsubscribeMaxDepth))
			}
			p.processRxUnsubscribe(msg)

		case msg := <-p.rxUpdate:
			p.log().Debugf("Received UPDATE message")
			// Track depth of processRxUpdate channel for monitoring
			currentDepth := len(p.rxUpdate)
			if currentDepth > p.maxRxChanUpdateMaxDepth {
				p.maxRxChanUpdateMaxDepth = currentDepth
				metricRxChanUpdateMaxDepth.WithLabelValues(p.id, p.remoteAddr).Set(float64(p.maxRxChanUpdateMaxDepth))
			}
			p.processRxUpdate(msg)
		case <-p.shutdown:
			p.cleanup()
			return
		}
	}
}

func (p *metalBondPeer) rxLoop() {
	p.wg.Add(1)
	defer p.wg.Done()

	var pktBuf []byte // Buffer to accumulate incoming bytes
	readTimeout := time.Duration(p.keepaliveInterval) * time.Second * 5 * 2

	for {
		if p.stopReceive {
			time.Sleep(1 * time.Second)
			continue
		}

		buf := make([]byte, 1220) // Max packet size (header + payload)

		// Set read deadline for the connection
		err := (*p.conn).SetReadDeadline(time.Now().Add(readTimeout))
		if err != nil {
			p.log().Errorf("Failed to set read deadline (timeout: %d): %v", readTimeout, err)
			go p.Reset()
			return
		}

		bytesRead, err := (*p.conn).Read(buf)
		if p.GetState() == CLOSED || p.GetState() == RETRY {
			return
		}

		// Handle read errors
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				p.log().Errorf("Read timeout (%d), resetting connection", readTimeout)
			} else if err == io.EOF {
				p.log().Infof("Connection closed by peer")
			} else {
				p.log().Errorf("Error reading from socket: %v", err)
			}
			go p.Reset()
			return
		}

		// Accumulate the received bytes
		pktBuf = append(pktBuf, buf[:bytesRead]...)

		// Process all complete packets in the buffer
		for {
			if len(pktBuf) < 4 {
				// Not enough data to read the 4-byte header
				break
			}

			// Extract the header fields
			pktVersion := pktBuf[0]
			pktLen := uint16(pktBuf[1])<<8 + uint16(pktBuf[2])
			pktType := MESSAGE_TYPE(pktBuf[3])
			totalPktLen := int(pktLen) + 4 // Full packet size (header + payload)

			// Sanity checks
			if pktVersion != 1 {
				p.log().Errorf("Unsupported protocol version: %d", pktVersion)
				go p.Reset()
				return
			}
			if pktLen > 1188 {
				p.log().Errorf("Payload length exceeds limit: %d", pktLen)
				go p.Reset()
				return
			}

			if len(pktBuf) < totalPktLen {
				// Incomplete packet, wait for more data
				break
			}

			// Extract the full packet
			pkt := pktBuf[:totalPktLen]
			pktBuf = pktBuf[totalPktLen:] // Remove processed packet from buffer

			// Extract the payload (protobuf message)
			pktPayload := pkt[4:] // Skip 4-byte header

			// Dispatch packet based on type
			switch pktType {
			case HELLO:
				hello, err := deserializeHelloMsg(pktPayload)
				if err != nil {
					p.log().Errorf("Cannot deserialize HELLO message: %v", err)
					go p.Reset()
					return
				}
				p.rxHello <- *hello

			case KEEPALIVE:
				p.rxKeepalive <- msgKeepalive{}

			case SUBSCRIBE:
				sub, err := deserializeSubscribeMsg(pktPayload)
				if err != nil {
					p.log().Errorf("Cannot deserialize SUBSCRIBE message: %v", err)
					go p.Reset()
					return
				}
				p.rxSubscribe <- *sub

			case UNSUBSCRIBE:
				unsub, err := deserializeUnsubscribeMsg(pktPayload)
				if err != nil {
					p.log().Errorf("Cannot deserialize UNSUBSCRIBE message: %v", err)
					go p.Reset()
					return
				}
				p.rxUnsubscribe <- *unsub

			case UPDATE:
				upd, err := deserializeUpdateMsg(pktPayload)
				if err != nil {
					p.log().Errorf("Cannot deserialize UPDATE message: %v", err)
					go p.Reset()
					return
				}
				p.rxUpdate <- *upd

			default:
				p.log().Errorf("Unknown message type: %d. Closing connection.", pktType)
				go p.Reset()
				return
			}
		}
	}
}

func (p *metalBondPeer) processRxHello(msg msgHello) {
	if msg.KeepaliveInterval < 1 {
		p.log().Errorf("Keepalive Interval too low (%d)", msg.KeepaliveInterval)
		p.Reset()
	}

	// Use lower Keepalive interval of both client and server as peer config
	p.isServer = msg.IsServer
	keepaliveInterval := p.keepaliveInterval
	if msg.KeepaliveInterval < keepaliveInterval {
		keepaliveInterval = msg.KeepaliveInterval
	}
	p.keepaliveInterval = keepaliveInterval

	// state transition to HELLO_RECEIVED
	p.setState(HELLO_RECEIVED)
	p.log().Infof("HELLO message received")

	go p.keepaliveLoop()

	// if direction incoming, send HELLO response
	if p.direction == INCOMING {
		helloMsg := msgHello{
			KeepaliveInterval: p.keepaliveInterval,
			IsServer:          p.metalbond.isServer,
		}

		if err := p.sendMessage(helloMsg); err != nil {
			p.log().Errorf("Failed to send message: %v", err)
		}
		p.setState(HELLO_SENT)
		p.log().Infof("HELLO message sent")
	}
}

func (p *metalBondPeer) processRxKeepalive(msg msgKeepalive) {
	if p.direction == INCOMING && p.GetState() == HELLO_SENT {
		p.log().Infof("Connection established")
		p.setState(ESTABLISHED)
	} else if p.direction == OUTGOING && p.GetState() == HELLO_RECEIVED {
		p.log().Infof("Connection established")
		p.setState(ESTABLISHED)
	} else if p.GetState() == ESTABLISHED {
		// all good
	} else {
		p.log().Errorf("Received KEEPALIVE while in wrong state. Closing connection.")
		go p.Reset()
		return
	}

	p.lastKeepaliveReceived = time.Now()
	p.resetKeepaliveTimeout()

	// The server must respond incoming KEEPALIVE messages with an own KEEPALIVE message.
	if p.direction == INCOMING {
		if err := p.sendMessage(msgKeepalive{}); err != nil {
			p.log().Errorf("Failed to send message: %v", err)
		}
	}
}

func (p *metalBondPeer) processRxSubscribe(msg msgSubscribe) {
	if p.GetState() == ESTABLISHED {
		p.log().Debugf("processRxSubscribe %#v", msg)
		p.mtxSubscribedVNIs.Lock()
		defer p.mtxSubscribedVNIs.Unlock()
		p.subscribedVNIs[msg.VNI] = true

		// Update subscription metric per peer
		metricSubscriptionCount.WithLabelValues(p.id, p.remoteAddr).Inc()

		if err := p.metalbond.addSubscriber(p, msg.VNI); err != nil {
			p.log().Errorf("Failed to add subscriber: %v", err)
		}
	} else {
		p.log().Errorf("Received Subscribe message in invalid state: %s", p.GetState().String())
	}
}

func (p *metalBondPeer) processRxUnsubscribe(msg msgUnsubscribe) {
	if p.GetState() == ESTABLISHED {
		p.log().Debugf("processRxUnsubscribe %#v", msg)
		if err := p.metalbond.removeSubscriber(p, msg.VNI); err != nil {
			p.log().Errorf("Failed to remove subscriber: %v", err)
		}

		p.mtxSubscribedVNIs.Lock()
		delete(p.subscribedVNIs, msg.VNI)
		// Update subscription metric per peer
		metricSubscriptionCount.WithLabelValues(p.id, p.remoteAddr).Dec()
		p.mtxSubscribedVNIs.Unlock()
	} else {
		p.log().Errorf("Received Unsubscribe message in invalid state: %s", p.GetState().String())
	}
}

func (p *metalBondPeer) processRxUpdate(msg msgUpdate) {
	var err error
	if p.GetState() == ESTABLISHED {
		switch msg.Action {
		case ADD:
			err = p.receivedRoutes.AddNextHop(msg.VNI, msg.Destination, msg.NextHop, p)
			if err != nil {
				p.log().Errorf("Could not add received route (%v -> %v) to peer's receivedRoutes Table: %v", msg.Destination, msg.NextHop, err)
				return
			}

			err = p.metalbond.addReceivedRoute(p, msg.VNI, msg.Destination, msg.NextHop)
			if err != nil {
				p.log().Errorf("Could not process received route UPDATE ADD: %v", err)
			}
		case REMOVE:
			err, _ = p.receivedRoutes.RemoveNextHop(msg.VNI, msg.Destination, msg.NextHop, p)
			if err != nil {
				p.log().Errorf("Could not remove received route from peer's receivedRoutes Table: %v", err)
				return
			}

			err = p.metalbond.removeReceivedRoute(p, msg.VNI, msg.Destination, msg.NextHop)
			if err != nil {
				p.log().Errorf("Could not process received route UPDATE REMOVE: %v", err)
			}
		default:
			p.log().Errorf("Received UPDATE message with invalid action type!")
		}
	} else {
		p.log().Errorf("Received Update message in invalid state: %s", p.GetState().String())
	}
}

func (p *metalBondPeer) Close() {
	p.log().Debug("Close")
	if p.GetState() != CLOSED {
		p.setState(CLOSED)
	}

	p.txChanClose <- true
	p.shutdown <- true
	p.keepaliveStop <- true
}

func (p *metalBondPeer) Reset() {
	p.log().Debugf("Reset")
	p.mtxReset.Lock()
	defer p.mtxReset.Unlock()

	if p.manuallyRemoved {
		// If we know this peer is manually removed, skip reconnect logic entirely.
		return
	}

	if p.GetState() == CLOSED {
		p.log().Debug("State is closed")
		return
	}

	switch p.direction {
	case INCOMING:
		// incoming connections are closed by the server
		// in order to avoid stale peer threads
		p.Close()
		if err := p.metalbond.RemovePeer(p.remoteAddr); err != nil {
			p.log().Errorf("Failed to remove peer: %v", err)
		}
	case OUTGOING:
		p.log().Infof("Resetting connection...")
		p.setState(RETRY)
		p.txChanClose <- true
		p.shutdown <- true
		p.keepaliveStop <- true
		p.wg.Wait()

		p.conn = nil
		p.localAddr = ""
		retry := time.Duration(rand.Intn(RetryIntervalMax)+RetryIntervalMin) * time.Second
		p.log().Infof("Closed. Waiting %s...", retry)

		time.Sleep(retry)

		// Double-check
		if p.manuallyRemoved {
			return
		}

		p.setState(CONNECTING)
		p.log().Infof("Reconnecting...")

		go p.handle()
	}
}

func (p *metalBondPeer) keepaliveLoop() {
	p.wg.Add(1)
	defer p.wg.Done()

	timeout := time.Duration(p.keepaliveInterval) * time.Second * 5 / 2
	p.log().Infof("KEEPALIVE timeout: %v", timeout)
	p.keepaliveTimer = time.NewTimer(timeout)

	interval := time.Duration(p.keepaliveInterval) * time.Second
	tckr := time.NewTicker(interval)

	// Sending initial KEEPALIVE message
	if p.direction == OUTGOING {
		p.lastKeepaliveSent = time.Now()
		if err := p.sendMessage(msgKeepalive{}); err != nil {
			p.log().Errorf("Failed to send message: %v", err)
		}
	}

	for {
		select {
		// Ticker triggers sending KEEPALIVE messages
		case <-tckr.C:
			if p.direction == OUTGOING {
				p.lastKeepaliveSent = time.Now()
				if err := p.sendMessage(msgKeepalive{}); err != nil {
					p.log().Errorf("Failed to send message: %v", err)
				}
			}

		// Timer detects KEEPALIVE timeouts
		case <-p.keepaliveTimer.C:
			p.log().Infof("Connection timed out. Closing.")
			go p.Reset()

		// keepaliveStop chan delivers message to stop this routine
		case <-p.keepaliveStop:
			p.log().Infof("Stopping keepaliveLoop")
			p.keepaliveTimer.Stop()
			return
		}
	}
}

func (p *metalBondPeer) resetKeepaliveTimeout() {
	if p.keepaliveTimer != nil {
		p.keepaliveTimer.Reset(time.Duration(p.keepaliveInterval) * time.Second * 5 / 2)
	}
}

func (p *metalBondPeer) sendMessage(msg message) error {
	p.log().Tracef("sendMessage")
	if p.GetState() == CLOSED {
		err := errors.New("State is closed")
		p.log().Debug(err)
		return err
	}

	var msgType MESSAGE_TYPE
	switch msg.(type) {
	case msgHello:
		msgType = HELLO
		p.log().Debugf("Sending HELLO message")
	case msgKeepalive:
		msgType = KEEPALIVE
		p.log().Tracef("Sending KEEPALIVE message")
	case msgSubscribe:
		msgType = SUBSCRIBE
		p.log().Debugf("Sending SUBSCRIBE message")
	case msgUnsubscribe:
		msgType = UNSUBSCRIBE
		p.log().Debugf("Sending UNSUBSCRIBE message")
	case msgUpdate:
		msgType = UPDATE
		p.log().Debugf("Sending UPDATE message")
	default:
		return fmt.Errorf("Unknown message type")
	}

	msgBytes, err := msg.Serialize()
	if err != nil {
		return fmt.Errorf("Could not serialize message: %v", err)
	}

	hdr := []byte{1, byte(len(msgBytes) >> 8), byte(len(msgBytes) % 256), byte(msgType)}
	pkt := append(hdr, msgBytes...)

	// Track metrics
	peerID := p.remoteAddr
	localID := p.id
	currentDepth := len(p.txChan)
	metricTxChanDepth.WithLabelValues(localID, peerID).Set(float64(currentDepth))

	// Update max depth if the current depth is the highest observed
	if currentDepth > p.maxTxChanDepth {
		p.maxTxChanDepth = currentDepth
		metricTxChanMaxDepth.WithLabelValues(localID, peerID).Set(float64(p.maxTxChanDepth))
	}

	p.txChan <- pkt

	return nil
}

func (p *metalBondPeer) txLoop() {
	p.wg.Add(1)
	defer p.wg.Done()

	writeTimeout := 5 * time.Second

	for {
		select {
		case msg := <-p.txChan:
			// Set a write deadline before each message
			err := (*p.conn).SetWriteDeadline(time.Now().Add(writeTimeout))
			if err != nil {
				p.log().Errorf("Error setting write deadline: %v", err)
				go p.Reset()
				return
			}

			// Write the message
			n, err := (*p.conn).Write(msg)
			if n != len(msg) || err != nil {
				p.log().Errorf("Could not transmit message completely: %v", err)
				go p.Reset()
			}
		case <-p.txChanClose:
			p.log().Infof("Closing TCP connection")
			(*p.conn).Close()
			return
		}
	}
}
