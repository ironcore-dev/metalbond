// Copyright 2022 OnMetal authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package metalbond

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

var RETRY_INTERVAL = time.Duration(5 * time.Second)

type metalBondPeer struct {
	conn       *net.Conn
	remoteAddr string
	direction  ConnectionDirection
	isServer   bool

	mtxReset sync.RWMutex
	mtxState sync.RWMutex
	state    ConnectionState

	receivedRoutes routeTable
	subscribedVNIs map[VNI]bool

	metalbond *MetalBond

	keepaliveInterval uint32
	keepaliveTimer    *time.Timer

	shutdown      chan bool
	keepaliveStop chan bool
	txChan        chan []byte
	rxHello       chan msgHello
	rxKeepalive   chan msgKeepalive
	rxSubscribe   chan msgSubscribe
	rxUnsubscribe chan msgUnsubscribe
	rxUpdate      chan msgUpdate
	wg            sync.WaitGroup
}

func newMetalBondPeer(
	pconn *net.Conn,
	remoteAddr string,
	keepaliveInterval uint32,
	direction ConnectionDirection,
	metalbond *MetalBond) *metalBondPeer {

	peer := metalBondPeer{
		conn:              pconn,
		remoteAddr:        remoteAddr,
		direction:         direction,
		state:             CONNECTING,
		receivedRoutes:    newRouteTable(),
		subscribedVNIs:    make(map[VNI]bool),
		keepaliveInterval: keepaliveInterval,
		metalbond:         metalbond,
	}

	go peer.handle()

	return &peer
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

					err := p.SendUpdate(upd)
					if err != nil {
						p.log().Debugf("Could not send update to peer: %v", err)
					}
				}
			}
		}
	}

	// Connection lost
	if oldState != newState && newState != ESTABLISHED {
		for vni, peers := range p.metalbond.subscribers {
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
	return logrus.WithField("peer", p.remoteAddr).WithField("state", p.GetState().String())
}

func (p *metalBondPeer) cleanup() {
	p.log().Debugf("cleanup")

	// unsubscribe from VNIs
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
}

func (p *metalBondPeer) handle() {
	p.wg.Add(1)
	defer p.wg.Done()

	p.txChan = make(chan []byte, 65536)
	p.shutdown = make(chan bool, 1)
	p.keepaliveStop = make(chan bool, 1)
	p.rxHello = make(chan msgHello, 50)
	p.rxKeepalive = make(chan msgKeepalive, 50)
	p.rxSubscribe = make(chan msgSubscribe, 1000)
	p.rxUnsubscribe = make(chan msgUnsubscribe, 1000)
	p.rxUpdate = make(chan msgUpdate, 1000)

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

		conn, err := net.Dial("tcp", p.remoteAddr)
		if err != nil {
			logrus.Infof("Cannot connect to server - %v - retry in %v", err, RETRY_INTERVAL)
			time.Sleep(RETRY_INTERVAL)
			continue
		}

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
			p.processRxHello(msg)

		case msg := <-p.rxKeepalive:
			p.log().Tracef("Received KEEPALIVE message")
			p.processRxKeepalive(msg)

		case msg := <-p.rxSubscribe:
			p.log().Debugf("Received SUBSCRIBE message")
			p.processRxSubscribe(msg)

		case msg := <-p.rxUnsubscribe:
			p.log().Debugf("Received UNSUBSCRIBE message")
			p.processRxUnsubscribe(msg)

		case msg := <-p.rxUpdate:
			p.log().Debugf("Received UPDATE message")
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

	buf := make([]byte, 65535)

	for {
		// TODO: read full packets!!!!
		bytesRead, err := (*p.conn).Read(buf)
		if p.GetState() == CLOSED || p.GetState() == RETRY {
			return
		}
		if err != nil {
			if err == io.EOF {
				p.log().Infof("Connection closed by peer")
				go p.Reset()
				return
			}
			logrus.Errorf("Error reading from socket: %v", err)
			go p.Reset()
			return
		}
		if bytesRead >= len(buf) {
			p.log().Warningf("too many messages in inbound queue. Closing connection.")
			go p.Reset()
			return
		}

		pktStart := 0

	parsePacket:
		pktVersion := buf[pktStart]
		switch pktVersion {
		case 1:
			pktLen := uint16(buf[pktStart+1])<<8 + uint16(buf[pktStart+2])
			pktType := MESSAGE_TYPE(buf[pktStart+3])
			pktBytes := buf[pktStart+4 : pktStart+int(pktLen)+4]

			pktStart += 4 + int(pktLen)

			switch pktType {
			case HELLO:
				hello, err := deserializeHelloMsg(pktBytes)
				if err != nil {
					p.log().Errorf("Cannot deserialize HELLO message: %v", err)
					go p.Reset()
					return
				}

				p.rxHello <- *hello

			case KEEPALIVE:
				p.rxKeepalive <- msgKeepalive{}

			case SUBSCRIBE:
				sub, err := deserializeSubscribeMsg(pktBytes)
				if err != nil {
					p.log().Errorf("Cannot deserialize SUBSCRIBE message: %v", err)
					go p.Reset()
					return
				}

				p.rxSubscribe <- *sub

			case UNSUBSCRIBE:
				sub, err := deserializeUnsubscribeMsg(pktBytes)
				if err != nil {
					p.log().Errorf("Cannot deserialize UNSUBSCRIBE message: %v", err)
					go p.Reset()
					return
				}

				p.rxUnsubscribe <- *sub

			case UPDATE:
				upd, err := deserializeUpdateMsg(pktBytes)
				if err != nil {
					p.log().Errorf("Cannot deserialize UPDATE message: %v", err)
					go p.Reset()
					return
				}
				p.rxUpdate <- *upd

			default:
				p.log().Errorf("Unknown Packet received. Closing connection.")
				return
			}
		default:
			p.log().Errorf("Incompatible Client version. Closing connection.")
			go p.Reset()
			return
		}

		if pktStart < bytesRead {
			goto parsePacket
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

	p.resetKeepaliveTimeout()

	// The server must respond incoming KEEPALIVE messages with an own KEEPALIVE message.
	if p.direction == INCOMING {
		if err := p.sendMessage(msgKeepalive{}); err != nil {
			p.log().Errorf("Failed to send message: %v", err)
		}
	}
}

func (p *metalBondPeer) processRxSubscribe(msg msgSubscribe) {
	p.log().Debugf("processRxSubscribe %#v", msg)
	p.subscribedVNIs[msg.VNI] = true
	if err := p.metalbond.addSubscriber(p, msg.VNI); err != nil {
		p.log().Errorf("Failed to add subscriber: %v", err)
	}
}

func (p *metalBondPeer) processRxUnsubscribe(msg msgUnsubscribe) {
	p.log().Debugf("processRxUnsubscribe %#v", msg)
	if err := p.metalbond.removeSubscriber(p, msg.VNI); err != nil {
		p.log().Errorf("Failed to remove subscriber: %v", err)
	}

	delete(p.subscribedVNIs, msg.VNI)
}

func (p *metalBondPeer) processRxUpdate(msg msgUpdate) {
	var err error

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
}

func (p *metalBondPeer) Close() {
	p.log().Debug("Close")
	if p.GetState() != CLOSED {
		p.setState(CLOSED)
		p.shutdown <- true
		p.keepaliveStop <- true
		close(p.txChan)
	}
}

func (p *metalBondPeer) Reset() {
	p.log().Debugf("Reset")
	p.mtxReset.RLock()
	defer p.mtxReset.RUnlock()

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
		close(p.txChan)
		p.shutdown <- true
		p.keepaliveStop <- true
		p.wg.Wait()

		p.conn = nil
		p.log().Infof("Closed. Waiting %s...", RETRY_INTERVAL)

		time.Sleep(RETRY_INTERVAL)
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
		if err := p.sendMessage(msgKeepalive{}); err != nil {
			p.log().Errorf("Failed to send message: %v", err)
		}
	}

	for {
		select {
		// Ticker triggers sending KEEPALIVE messages
		case <-tckr.C:
			if p.direction == OUTGOING {
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

	p.txChan <- pkt

	return nil
}

func (p *metalBondPeer) txLoop() {
	p.wg.Add(1)
	defer p.wg.Done()

	for {
		msg, more := <-p.txChan
		if !more {
			p.log().Debugf("Closing TCP connection")
			(*p.conn).Close()
			return
		}

		n, err := (*p.conn).Write(msg)
		if n != len(msg) || err != nil {
			p.log().Errorf("Could not transmit message completely: %v", err)
			go p.Reset()
		}
	}

}
