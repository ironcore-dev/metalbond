// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package metalbond

import (
	"fmt"
	"sync"
)

type routeTable struct {
	rwmtx  sync.RWMutex
	routes map[VNI]map[Destination]map[NextHop]map[*metalBondPeer]bool
}

func newRouteTable() routeTable {
	return routeTable{
		routes: make(map[VNI]map[Destination]map[NextHop]map[*metalBondPeer]bool),
	}
}

func (rt *routeTable) GetVNIs() []VNI {
	rt.rwmtx.RLock()
	defer rt.rwmtx.RUnlock()

	vnis := []VNI{}
	for k := range rt.routes {
		vnis = append(vnis, k)
	}
	return vnis
}

func (rt *routeTable) GetDestinationsByVNI(vni VNI) map[Destination][]NextHop {
	rt.rwmtx.RLock()
	defer rt.rwmtx.RUnlock()

	ret := make(map[Destination][]NextHop)

	if _, exists := rt.routes[vni]; !exists {
		return ret
	}

	for dest, nhm := range rt.routes[vni] {
		nhs := []NextHop{}

		for nh := range nhm {
			nhs = append(nhs, nh)
		}

		ret[dest] = nhs
	}

	return ret
}

func (rt *routeTable) GetDestinationsByVNIWithPeer(vni VNI) map[Destination]map[NextHop][]*metalBondPeer {
	rt.rwmtx.RLock()
	defer rt.rwmtx.RUnlock()

	ret := make(map[Destination]map[NextHop][]*metalBondPeer)

	if _, exists := rt.routes[vni]; !exists {
		return ret
	}

	for dest, nhm := range rt.routes[vni] {
		if ret[dest] == nil {
			ret[dest] = make(map[NextHop][]*metalBondPeer)
		}

		for nh, peersMap := range nhm {
			peers := []*metalBondPeer{}
			for peer := range peersMap {
				peers = append(peers, peer)
			}
			ret[dest][nh] = peers
		}
	}

	return ret
}

func (rt *routeTable) GetNextHopsByDestination(vni VNI, dest Destination) []NextHop {
	rt.rwmtx.RLock()
	defer rt.rwmtx.RUnlock()

	nh := []NextHop{}

	// TODO Performance: reused found map pointers
	if _, exists := rt.routes[vni]; !exists {
		return nh
	}

	if _, exists := rt.routes[vni][dest]; !exists {
		return nh
	}

	for k := range rt.routes[vni][dest] {
		nh = append(nh, k)
	}

	return nh
}

func (rt *routeTable) RemoveNextHop(vni VNI, dest Destination, nh NextHop, receivedFrom *metalBondPeer) (error, int) {
	rt.rwmtx.Lock()
	defer rt.rwmtx.Unlock()

	if rt.routes == nil {
		rt.routes = make(map[VNI]map[Destination]map[NextHop]map[*metalBondPeer]bool)
	}

	// TODO Performance: reused found map pointers
	if _, exists := rt.routes[vni]; !exists {
		return fmt.Errorf("VNI does not exist"), 0
	}

	if _, exists := rt.routes[vni][dest]; !exists {
		return fmt.Errorf("Destination does not exist"), 0
	}

	if _, exists := rt.routes[vni][dest][nh]; !exists {
		return fmt.Errorf("Nexthop does not exist"), 0
	}

	if _, exists := rt.routes[vni][dest][nh][receivedFrom]; !exists {
		return fmt.Errorf("ReceivedFrom does not exist"), 0
	}

	delete(rt.routes[vni][dest][nh], receivedFrom)
	left := len(rt.routes[vni][dest][nh])

	if len(rt.routes[vni][dest][nh]) == 0 {
		delete(rt.routes[vni][dest], nh)
	}

	if len(rt.routes[vni][dest]) == 0 {
		delete(rt.routes[vni], dest)
	}

	if len(rt.routes[vni]) == 0 {
		delete(rt.routes, vni)
	}

	return nil, left
}

func (rt *routeTable) AddNextHop(vni VNI, dest Destination, nh NextHop, receivedFrom *metalBondPeer) error {
	rt.rwmtx.Lock()
	defer rt.rwmtx.Unlock()

	// TODO Performance: reused found map pointers
	if _, exists := rt.routes[vni]; !exists {
		rt.routes[vni] = make(map[Destination]map[NextHop]map[*metalBondPeer]bool)
	}

	if _, exists := rt.routes[vni][dest]; !exists {
		rt.routes[vni][dest] = make(map[NextHop]map[*metalBondPeer]bool)
	}

	if _, exists := rt.routes[vni][dest][nh]; !exists {
		rt.routes[vni][dest][nh] = make(map[*metalBondPeer]bool)
	}

	if _, exists := rt.routes[vni][dest][nh][receivedFrom]; exists {
		return fmt.Errorf("Nexthop already exists")
	}

	rt.routes[vni][dest][nh][receivedFrom] = true

	return nil
}

func (rt *routeTable) NextHopExists(vni VNI, dest Destination, nh NextHop, receivedFrom *metalBondPeer) bool {
	rt.rwmtx.Lock()
	defer rt.rwmtx.Unlock()

	// TODO Performance: reused found map pointers
	if _, exists := rt.routes[vni]; !exists {
		rt.routes[vni] = make(map[Destination]map[NextHop]map[*metalBondPeer]bool)
	}

	if _, exists := rt.routes[vni][dest]; !exists {
		rt.routes[vni][dest] = make(map[NextHop]map[*metalBondPeer]bool)
	}

	if _, exists := rt.routes[vni][dest][nh]; !exists {
		rt.routes[vni][dest][nh] = make(map[*metalBondPeer]bool)
	}

	if _, exists := rt.routes[vni][dest][nh][receivedFrom]; exists {
		return true
	}

	return false
}
