// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package metalbond

import (
	"fmt"
	"net/netip"
	"sync"
)

// routeTable maintains the routing structure
type routeTable struct {
	rwmtx  sync.RWMutex
	routes map[VNI]map[string]map[NextHop]map[*metalBondPeer]bool
}

// newRouteTable initializes a new route table
func newRouteTable() routeTable {
	return routeTable{
		routes: make(map[VNI]map[string]map[NextHop]map[*metalBondPeer]bool),
	}
}

// deriveIPVersion determines the IPVersion from a prefix
func deriveIPVersion(prefix netip.Prefix) IPVersion {
	if prefix.Addr().Is4() {
		return IPV4
	}
	return IPV6
}

// GetVNIs returns the VNIs available in the route table
func (rt *routeTable) GetVNIs() []VNI {
	rt.rwmtx.RLock()
	defer rt.rwmtx.RUnlock()

	vnis := []VNI{}
	for k := range rt.routes {
		vnis = append(vnis, k)
	}
	return vnis
}

// GetDestinationsByVNI returns destinations by VNI as map[Destination][]NextHop
func (rt *routeTable) GetDestinationsByVNI(vni VNI) map[Destination][]NextHop {
	rt.rwmtx.RLock()
	defer rt.rwmtx.RUnlock()

	ret := make(map[Destination][]NextHop)

	if _, exists := rt.routes[vni]; !exists {
		return ret
	}

	for destStr, nhm := range rt.routes[vni] {
		prefix := netip.MustParsePrefix(destStr)
		dest := Destination{
			Prefix:    prefix,
			IPVersion: deriveIPVersion(prefix),
		}

		nhs := []NextHop{}
		for nh := range nhm {
			nhs = append(nhs, nh)
		}
		ret[dest] = nhs
	}

	return ret
}

// GetDestinationsByVNIWithPeer returns destinations by VNI along with next hop peers
func (rt *routeTable) GetDestinationsByVNIWithPeer(vni VNI) map[Destination]map[NextHop][]*metalBondPeer {
	rt.rwmtx.RLock()
	defer rt.rwmtx.RUnlock()

	ret := make(map[Destination]map[NextHop][]*metalBondPeer)

	if _, exists := rt.routes[vni]; !exists {
		return ret
	}

	for destStr, nhm := range rt.routes[vni] {
		prefix := netip.MustParsePrefix(destStr)
		dest := Destination{
			Prefix:    prefix,
			IPVersion: deriveIPVersion(prefix),
		}

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

// GetNextHopsByDestination returns next hops by destination
func (rt *routeTable) GetNextHopsByDestination(vni VNI, dest Destination) []NextHop {
	rt.rwmtx.RLock()
	defer rt.rwmtx.RUnlock()

	nh := []NextHop{}
	destKey := dest.Prefix.String()

	if _, exists := rt.routes[vni]; !exists {
		return nh
	}

	if _, exists := rt.routes[vni][destKey]; !exists {
		return nh
	}

	for k := range rt.routes[vni][destKey] {
		nh = append(nh, k)
	}

	return nh
}

// RemoveNextHop removes a next hop from the route table
func (rt *routeTable) RemoveNextHop(vni VNI, dest Destination, nh NextHop, receivedFrom *metalBondPeer) (error, int) {
	rt.rwmtx.Lock()
	defer rt.rwmtx.Unlock()

	destKey := dest.Prefix.String()

	if _, exists := rt.routes[vni]; !exists {
		return fmt.Errorf("VNI does not exist"), 0
	}

	if _, exists := rt.routes[vni][destKey]; !exists {
		return fmt.Errorf("Destination does not exist"), 0
	}

	if _, exists := rt.routes[vni][destKey][nh]; !exists {
		return fmt.Errorf("Nexthop does not exist"), 0
	}

	left := 0
	if receivedFrom == nil {
		delete(rt.routes[vni][destKey], nh)
	} else {
		if _, exists := rt.routes[vni][destKey][nh][receivedFrom]; !exists {
			return fmt.Errorf("ReceivedFrom does not exist"), 0
		}

		delete(rt.routes[vni][destKey][nh], receivedFrom)
		left = len(rt.routes[vni][destKey][nh])

		if left == 0 {
			delete(rt.routes[vni][destKey], nh)
		}
	}

	if len(rt.routes[vni][destKey]) == 0 {
		delete(rt.routes[vni], destKey)
	}

	if len(rt.routes[vni]) == 0 {
		delete(rt.routes, vni)
	}

	return nil, left
}

// AddNextHop adds a new next hop to the route table
func (rt *routeTable) AddNextHop(vni VNI, dest Destination, nh NextHop, receivedFrom *metalBondPeer) error {
	rt.rwmtx.Lock()
	defer rt.rwmtx.Unlock()

	destKey := dest.Prefix.String()

	if _, exists := rt.routes[vni]; !exists {
		rt.routes[vni] = make(map[string]map[NextHop]map[*metalBondPeer]bool)
	}

	if _, exists := rt.routes[vni][destKey]; !exists {
		rt.routes[vni][destKey] = make(map[NextHop]map[*metalBondPeer]bool)
	}

	if _, exists := rt.routes[vni][destKey][nh]; !exists {
		rt.routes[vni][destKey][nh] = make(map[*metalBondPeer]bool)
	}

	if _, exists := rt.routes[vni][destKey][nh][receivedFrom]; exists {
		return fmt.Errorf("Nexthop already exists")
	}

	rt.routes[vni][destKey][nh][receivedFrom] = true
	return nil
}

// NextHopExists checks if a next hop exists in the route table
func (rt *routeTable) NextHopExists(vni VNI, dest Destination, nh NextHop, receivedFrom *metalBondPeer) bool {
	rt.rwmtx.Lock()
	defer rt.rwmtx.Unlock()

	destKey := dest.Prefix.String()

	if _, exists := rt.routes[vni]; !exists {
		return false
	}

	if _, exists := rt.routes[vni][destKey]; !exists {
		return false
	}

	if _, exists := rt.routes[vni][destKey][nh]; !exists {
		return false
	}

	_, exists := rt.routes[vni][destKey][nh][receivedFrom]
	return exists
}
