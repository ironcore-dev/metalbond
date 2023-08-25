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
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/onmetal/metalbond/pb"
	"github.com/sirupsen/logrus"
)

type MetalBond struct {
	routeTable routeTable

	myAnnouncements routeTable

	mtxMySubscriptions sync.RWMutex
	mySubscriptions    map[VNI]bool

	mtxSubscribers sync.RWMutex                    // this locks a bit much (all VNIs). We could create a mutex for every VNI instead.
	subscribers    map[VNI]map[*metalBondPeer]bool // HashMap of HashSet

	mtxPeers sync.RWMutex
	peers    map[string]*metalBondPeer

	keepaliveInterval uint32
	shuttingDown      bool

	client Client

	lis      *net.Listener // for server only
	isServer bool
}

type Config struct {
	KeepaliveInterval uint32
}

func NewMetalBond(config Config, client Client) *MetalBond {
	if config.KeepaliveInterval == 0 {
		config.KeepaliveInterval = 5
	}

	m := MetalBond{
		routeTable:        newRouteTable(),
		myAnnouncements:   newRouteTable(),
		mySubscriptions:   make(map[VNI]bool),
		subscribers:       make(map[VNI]map[*metalBondPeer]bool),
		keepaliveInterval: config.KeepaliveInterval,
		peers:             map[string]*metalBondPeer{},
		client:            client,
	}

	return &m
}

func (m *MetalBond) StartHTTPServer(listen string) error {
	go serveJsonRouteTable(m, listen)

	return nil
}

func (m *MetalBond) AddPeer(addr, localIP string) error {
	m.mtxPeers.Lock()
	defer m.mtxPeers.Unlock()

	m.log().Infof("Adding peer %s", addr)
	if _, exists := m.peers[addr]; exists {
		return fmt.Errorf("Peer already registered")
	}

	m.peers[addr] = newMetalBondPeer(
		nil,
		addr,
		localIP,
		m.keepaliveInterval,
		OUTGOING,
		m)

	return nil
}

func (m *MetalBond) RemovePeer(addr string) error {
	m.log().Debugf("RemovePeer %s", addr)
	m.unsafeRemovePeer(addr)
	return nil
}

func (m *MetalBond) unsafeRemovePeer(addr string) {
	m.log().Infof("Removing peer %s", addr)
	m.mtxPeers.RLock()
	p, exists := m.peers[addr]
	m.mtxPeers.RUnlock()
	if !exists {
		m.log().Errorf("Peer %s does not exist", addr)
	} else {
		p.Close()

		m.mtxPeers.Lock()
		delete(m.peers, addr)
		m.mtxPeers.Unlock()
	}
}

func (m *MetalBond) PeerState(addr string) (ConnectionState, error) {
	m.mtxPeers.RLock()
	defer m.mtxPeers.RUnlock()

	m.log().Debugf("PeerState peer %s", addr)
	if _, exists := m.peers[addr]; exists {
		state := m.peers[addr].GetState()
		return state, nil
	} else {
		return CLOSED, fmt.Errorf("Peer %s does not exist", addr)
	}
}

func (m *MetalBond) Subscribe(vni VNI) error {
	m.mtxMySubscriptions.Lock()
	defer func() {
		m.mtxMySubscriptions.Unlock()
		time.Sleep(1 * time.Second)
	}()

	if _, exists := m.mySubscriptions[vni]; exists {
		return fmt.Errorf("Already subscribed to VNI %d", vni)
	}

	m.mySubscriptions[vni] = true

	for _, p := range m.peers {
		if err := p.Subscribe(vni); err != nil {
			return fmt.Errorf("Could not subscribe to vni %d: %v", vni, err)
		}
	}

	return nil
}

func (m *MetalBond) IsSubscribed(vni VNI) bool {
	m.mtxMySubscriptions.Lock()
	defer m.mtxMySubscriptions.Unlock()

	if _, exists := m.mySubscriptions[vni]; exists {
		return true
	}

	return false
}

func (m *MetalBond) Unsubscribe(vni VNI) error {
	m.mtxMySubscriptions.Lock()
	defer m.mtxMySubscriptions.Unlock()

	if _, exists := m.mySubscriptions[vni]; !exists {
		return fmt.Errorf("Already unsubscribed from VNI %d", vni)
	}

	for _, p := range m.peers {
		if err := p.Unsubscribe(vni); err != nil {
			m.log().Errorf("Could not unsubscribe from vni: %v", err)
		}

		// remove from local route table
		for dest, nhs := range m.routeTable.GetDestinationsByVNI(vni) {
			for _, nh := range nhs {
				if m.routeTable.NextHopExists(vni, dest, nh, p) {
					if err, _ := m.routeTable.RemoveNextHop(vni, dest, nh, p); err != nil {
						p.log().Errorf("Could not remove received route from peer's receivedRoutes Table: %v", err)
					}
				}
			}
		}
	}

	delete(m.mySubscriptions, vni)

	return nil
}

func (m *MetalBond) IsRouteAnnounced(vni VNI, dest Destination, hop NextHop) bool {
	return m.myAnnouncements.NextHopExists(vni, dest, hop, nil)
}

func (m *MetalBond) AnnounceRoute(vni VNI, dest Destination, hop NextHop) error {
	m.log().Infof("Announcing VNI %d: %s via %s", vni, dest, hop)

	if err := m.myAnnouncements.AddNextHop(vni, dest, hop, nil); err != nil {
		return fmt.Errorf("Cannot announce route: %v", err)
	}

	if err := m.distributeRouteToPeers(ADD, vni, dest, hop, nil); err != nil {
		return fmt.Errorf("Could not distribute route to peers: %v", err)
	}

	return nil
}

func (m *MetalBond) WithdrawRoute(vni VNI, dest Destination, hop NextHop) error {

	m.log().Infof("withdraw a route for VNI %d: %s via %s", vni, dest, hop)

	err, remaining := m.myAnnouncements.RemoveNextHop(vni, dest, hop, nil)
	if err != nil {
		return fmt.Errorf("cannot remove route from the local announcement route table: %v", err)
	}

	// TODO: Due to the internal complexity,
	// the logic, regarding when/how nexthops or entire routes are removed from metalbond under the cases in which withdrawing or receiving deleting route messages,
	// should be double-checked.
	if remaining == 0 {
		if err := m.distributeRouteToPeers(REMOVE, vni, dest, hop, nil); err != nil {
			m.log().Errorf("could not distribute route to peers: %v", err)
			return fmt.Errorf("failed to withdraw a route, for the reason: %v", err)
		}
	}

	return nil
}

func (m *MetalBond) getMyAnnouncements() *routeTable {
	return &m.myAnnouncements
}

func (m *MetalBond) distributeRouteToPeers(action UpdateAction, vni VNI, dest Destination, hop NextHop, fromPeer *metalBondPeer) error {
	m.mtxPeers.RLock()
	defer m.mtxPeers.RUnlock()

	m.mtxSubscribers.RLock()
	defer m.mtxSubscribers.RUnlock()

	// if this node is the origin of the route (fromPeer == nil):
	if fromPeer == nil {
		for _, sp := range m.peers {
			upd := msgUpdate{
				Action:      action,
				VNI:         vni,
				Destination: dest,
				NextHop:     hop,
			}

			err := sp.SendUpdate(upd)
			if err != nil {
				m.log().WithField("peer", sp).Debugf("Could not send update to peer: %v", err)
				return err
			}
		}
		return nil
	}

	// if no one has subscribed to this VNI, we don't need to distribute the route
	if _, exists := m.subscribers[vni]; !exists {
		return nil
	}

	// send route to all peers who have subscribed to this VNI - with few exceptions:
	for p := range m.subscribers[vni] {
		// don't send route back to the peer we got it from
		// Only for NAT routes
		if p == fromPeer && hop.Type == pb.NextHopType_NAT {
			continue
		}

		// TODO: Server to server communication
		if fromPeer.isServer && p.isServer {
			continue
		}

		upd := msgUpdate{
			Action:      action,
			VNI:         vni,
			Destination: dest,
			NextHop:     hop,
		}

		err := p.SendUpdate(upd)
		if err != nil {
			m.log().WithField("peer", p).Debugf("Could not send update to peer: %v", err)
		}
	}

	return nil
}

func (m *MetalBond) GetRoutesForVni(vni VNI) error {
	for dest, hops := range m.routeTable.GetDestinationsByVNI(vni) {
		for _, hop := range hops {
			err := m.client.AddRoute(vni, dest, hop)
			if err != nil {
				m.log().Errorf("Client.AddRoute call failed in Refill: %v", err)
				return err
			}
		}
	}
	return nil
}

func (m *MetalBond) addReceivedRoute(fromPeer *metalBondPeer, vni VNI, dest Destination, hop NextHop) error {
	err := m.routeTable.AddNextHop(vni, dest, hop, fromPeer)
	if err != nil {
		return fmt.Errorf("Cannot add route to route table: %v", err)
	}

	if hop.Type == pb.NextHopType_NAT {
		m.log().Infof("Received Route: VNI %d, Prefix: %s, NextHop: %s Type: %s PortFrom: %d PortTo: %d, from Peer %s", vni, dest, hop, hop.Type.String(), hop.NATPortRangeFrom, hop.NATPortRangeTo, fromPeer)
	} else {
		m.log().Infof("Received Route: VNI %d, Prefix: %s, NextHop: %s Type: %s, from Peer: %s", vni, dest, hop, hop.Type.String(), fromPeer)
	}

	if err := m.distributeRouteToPeers(ADD, vni, dest, hop, fromPeer); err != nil {
		m.log().Errorf("Could not distribute route to peers: %v", err)
	}

	err = m.client.AddRoute(vni, dest, hop)
	if err != nil {
		m.log().Errorf("Client.AddRoute call failed: %v", err)
	}

	return nil
}

func (m *MetalBond) removeReceivedRoute(fromPeer *metalBondPeer, vni VNI, dest Destination, hop NextHop) error {
	err, remaining := m.routeTable.RemoveNextHop(vni, dest, hop, fromPeer)
	if err != nil {
		return fmt.Errorf("Cannot remove route from route table: %v", err)
	}

	if hop.Type == pb.NextHopType_NAT {
		m.log().Infof("Removed Received Route: VNI %d, Prefix: %s, NextHop: %s Type: %s PortFrom: %d PortTo: %d, from Peer: %s", vni, dest, hop, hop.Type.String(), hop.NATPortRangeFrom, hop.NATPortRangeTo, fromPeer)
	} else {
		m.log().Infof("Removed Received Route: VNI %d, Prefix: %s, NextHop: %s Type: %s, from Peer: %s", vni, dest, hop, hop.Type.String(), fromPeer)
	}

	if remaining == 0 {
		if err := m.distributeRouteToPeers(REMOVE, vni, dest, hop, fromPeer); err != nil {
			m.log().Errorf("Could not distribute route to peers: %v", err)
		}
	}

	err = m.client.RemoveRoute(vni, dest, hop)
	if err != nil {
		m.log().Errorf("Client.RemoveRoute call failed: %v", err)
	}

	return nil
}

// addSubscriber is called by metalBondPeer when an SUBSCRIBE message has been received from the peer.
// Route updates belonging to the specified VNI will be sent to the peer afterwards.
func (m *MetalBond) addSubscriber(peer *metalBondPeer, vni VNI) error {
	m.log().Infof("addSubscriber(%s, %d)", peer, vni)
	m.mtxSubscribers.Lock()

	if _, exists := m.subscribers[vni]; !exists {
		m.subscribers[vni] = make(map[*metalBondPeer]bool)
	}

	if _, exists := m.subscribers[vni][peer]; exists {
		return fmt.Errorf("Peer is already subscribed!")
	}

	m.subscribers[vni][peer] = true
	m.mtxSubscribers.Unlock()

	m.log().Infof("Peer %s added Subscription to VNI %d", peer, vni)

	// TODO: we're missing a read-lock on routeTable
	for dest, hopToPeersMap := range m.routeTable.GetDestinationsByVNIWithPeer(vni) {
		for hop, peers := range hopToPeersMap {
			for _, peerFromList := range peers {
				// Dont send the NAT routes back to the original peer which announced it
				if peerFromList == peer && hop.Type == pb.NextHopType_NAT {
					continue
				}
				err := peer.SendUpdate(msgUpdate{
					Action:      ADD,
					VNI:         vni,
					Destination: dest,
					NextHop:     hop,
				})
				if err != nil {
					m.log().Errorf("Could not send UPDATE to peer: %v", err)
					peer.Reset()
				}
			}
		}
	}

	return nil
}

// removeSubscriber is called by metalBondPeer when an UNSUBSCRIBE message has been received from the peer.
func (m *MetalBond) removeSubscriber(peer *metalBondPeer, vni VNI) error {
	m.log().Infof("removeSubscriber(%s, %d)", peer, vni)

	m.mtxSubscribers.RLock()
	if _, exists := m.subscribers[vni]; !exists {
		m.mtxSubscribers.RUnlock()
		return fmt.Errorf("Peer is not subscribed!")
	}

	if _, exists := m.subscribers[vni][peer]; !exists {
		m.mtxSubscribers.RUnlock()
		return fmt.Errorf("Peer is not subscribed!")
	}
	m.mtxSubscribers.RUnlock()

	// remove routes from peer and local and distribute the remove
	peer.log().Infof("Removing all received nexthops from peer for vni %d", vni)
	for dest, nhs := range peer.receivedRoutes.GetDestinationsByVNI(vni) {
		for _, nh := range nhs {
			if err, _ := peer.receivedRoutes.RemoveNextHop(vni, dest, nh, peer); err != nil {
				peer.log().Errorf("Could not remove received route from peer's receivedRoutes Table: %v", err)
			}

			if err := m.removeReceivedRoute(peer, vni, dest, nh); err != nil {
				peer.log().Errorf("Cannot remove received route from metalbond db: %v", err)
			}
		}
	}

	m.mtxSubscribers.Lock()
	delete(m.subscribers[vni], peer)
	m.mtxSubscribers.Unlock()

	m.log().Infof("Peer %s removed Subscription from VNI %d", peer, vni)

	return nil
}

// StartServer starts the MetalBond server asynchronously.
// To stop the server again, call Shutdown().
func (m *MetalBond) StartServer(listenAddress string) error {
	lis, err := net.Listen("tcp", listenAddress)
	m.lis = &lis
	if err != nil {
		return fmt.Errorf("Cannot open TCP port: %v", err)
	}
	m.isServer = true

	m.log().Infof("Listening on %s", listenAddress)

	go func() {
		for {
			conn, err := lis.Accept()
			if m.shuttingDown {
				return
			} else if err != nil {
				m.log().Errorf("Error accepting incoming connection: %v", err)
				return
			}

			p := newMetalBondPeer(
				&conn,
				conn.RemoteAddr().String(),
				"",
				m.keepaliveInterval,
				INCOMING,
				m,
			)
			m.mtxPeers.Lock()
			m.log().Infof("New peer %s", conn.RemoteAddr().String())
			m.peers[conn.RemoteAddr().String()] = p
			m.mtxPeers.Unlock()
		}
	}()

	return nil
}

// Shutdown stops the MetalBond server.
func (m *MetalBond) Shutdown() {
	m.log().Infof("Shutting down MetalBond...")
	m.shuttingDown = true
	if m.lis != nil {
		(*m.lis).Close()
	}

	for p := range m.peers {
		m.unsafeRemovePeer(p)
	}
}

func (m *MetalBond) log() *logrus.Entry {
	return logrus.WithFields(nil)
}
