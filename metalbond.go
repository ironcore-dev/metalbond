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
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"sync"

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

func (m *MetalBond) AddPeer(addr string) error {
	m.mtxPeers.Lock()
	defer m.mtxPeers.Unlock()

	m.log().Infof("Adding peer %s", addr)
	if _, exists := m.peers[addr]; exists {
		return fmt.Errorf("peer already registered")
	}

	m.peers[addr] = newMetalBondPeer(
		nil,
		addr,
		m.keepaliveInterval,
		OUTGOING,
		m)

	return nil
}

func (m *MetalBond) RemovePeer(addr string) error {
	m.mtxPeers.Lock()
	defer m.mtxPeers.Unlock()

	return m.unsafeRemovePeer(addr)
}

func (m *MetalBond) unsafeRemovePeer(addr string) error {
	m.log().Infof("Removing peer %s", addr)
	if _, exists := m.peers[addr]; !exists {
		m.log().Errorf("Peer %s does not exist", addr)
		return nil
	}

	m.peers[addr].Close()

	delete(m.peers, addr)
	return nil
}

func (m *MetalBond) Subscribe(vni VNI) error {
	m.mtxMySubscriptions.Lock()
	defer m.mtxMySubscriptions.Unlock()

	if _, exists := m.mySubscriptions[vni]; exists {
		return fmt.Errorf("already subscribed to VNI %d", vni)
	}

	m.mySubscriptions[vni] = true

	for _, p := range m.peers {
		if err := p.Subscribe(vni); err != nil {
			m.log().Errorf("Could not subscribe to vni: %v", err)
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
	m.log().Errorf("Unsubscribe not implemented (VNI %d)", vni)
	return nil
}

func (m *MetalBond) IsRouteAnnounced(vni VNI, dest Destination, hop NextHop) bool {
	return m.myAnnouncements.NextHopExists(vni, dest, hop, nil)
}

func (m *MetalBond) AnnounceRoute(vni VNI, dest Destination, hop NextHop) error {
	m.log().Infof("Announcing VNI %d: %s via %s", vni, dest, hop)

	err := m.myAnnouncements.AddNextHop(vni, dest, hop, nil)
	if err != nil {
		return fmt.Errorf("cannot announce route: %v", err)
	}

	if err := m.distributeRouteToPeers(ADD, vni, dest, hop, nil); err != nil {
		m.log().Errorf("Could not distribute route to peers: %v", err)
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

	ctx := context.Background()
	// if this node is the origin of the route (fromPeer == nil):
	if fromPeer == nil {
		for _, sp := range m.peers {

			jsonObj, err := json.Marshal(RouteObject{Dest: dest,
				Hop: hop})
			if err != nil {
				return err
			}

			_, err = sp.redisClient.Publish(ctx, "vni/"+strconv.FormatUint(uint64(vni), 10), jsonObj).Result()
			if err != nil {
				fmt.Println("Error publishing message:", err)
			} else {
				fmt.Printf("Published message: %s\n", jsonObj)
			}

		}
		return nil
	}

	return nil
}

func (m *MetalBond) addReceivedRoute(fromPeer *metalBondPeer, vni VNI, dest Destination, hop NextHop) error {
	err := m.routeTable.AddNextHop(vni, dest, hop, fromPeer)
	if err != nil {
		return fmt.Errorf("Cannot add route to route table: %v", err)
	}

	if hop.Type == pb.NextHopType_NAT {
		m.log().Infof("Received Route: VNI %d, Prefix: %s, NextHop: %s Type: %s PortFrom: %d PortTo: %d", vni, dest, hop, hop.Type.String(), hop.NATPortRangeFrom, hop.NATPortRangeTo)
	} else {
		m.log().Infof("Received Route: VNI %d, Prefix: %s, NextHop: %s Type: %s", vni, dest, hop, hop.Type.String())
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
		m.log().Infof("Removed Received Route: VNI %d, Prefix: %s, NextHop: %s Type: %s PortFrom: %d PortTo: %d", vni, dest, hop, hop.Type.String(), hop.NATPortRangeFrom, hop.NATPortRangeTo)
	} else {
		m.log().Infof("Removed Received Route: VNI %d, Prefix: %s, NextHop: %s Type: %s", vni, dest, hop, hop.Type.String())
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

// StartServer starts the MetalBond server asynchronously.
// To stop the server again, call Shutdown().
func (m *MetalBond) StartServer(listenAddress string) error {
	m.isServer = true

	m.log().Infof("Starting server-client on %s", listenAddress)

	go func() {
		p := newMetalBondPeer(
			nil,
			listenAddress,
			m.keepaliveInterval,
			INCOMING,
			m,
		)
		m.mtxPeers.Lock()
		m.peers[listenAddress] = p
		m.mtxPeers.Unlock()
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

	m.mtxPeers.Lock()
	for p := range m.peers {
		if err := m.unsafeRemovePeer(p); err != nil {
			m.log().Errorf("Error removing peer %s: %v", p, err)
		}
	}
	m.mtxPeers.Unlock()
}

func (m *MetalBond) log() *logrus.Entry {
	return logrus.WithFields(nil)
}
