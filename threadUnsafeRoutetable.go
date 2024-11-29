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
)

type threadUnsafeRouteTable struct {
	routes map[VNI]map[Destination]map[NextHop]bool
}

func newThreadUnsafeRouteTable() threadUnsafeRouteTable {
	return threadUnsafeRouteTable{
		routes: make(map[VNI]map[Destination]map[NextHop]bool),
	}
}

func (rt *threadUnsafeRouteTable) GetVNIs() []VNI {

	vnis := []VNI{}
	for k := range rt.routes {
		vnis = append(vnis, k)
	}
	return vnis
}

func (rt *threadUnsafeRouteTable) GetDestinationsByVNI(vni VNI) map[Destination][]NextHop {

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

func (rt *threadUnsafeRouteTable) GetNextHopsByDestination(vni VNI, dest Destination) []NextHop {

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

func (rt *threadUnsafeRouteTable) RemoveNextHop(vni VNI, dest Destination, nh NextHop) (error, int) {

	if rt.routes == nil {
		rt.routes = make(map[VNI]map[Destination]map[NextHop]bool)
	}

	// TODO Performance: reused found map pointers
	if _, exists := rt.routes[vni]; !exists {
		return fmt.Errorf("Nexthop does not exist"), 0
	}

	if _, exists := rt.routes[vni][dest]; !exists {
		return fmt.Errorf("Nexthop does not exist"), 0
	}

	if _, exists := rt.routes[vni][dest][nh]; !exists {
		return fmt.Errorf("Nexthop does not exist"), 0
	}

	delete(rt.routes[vni][dest], nh)
	left := len(rt.routes[vni][dest])

	if len(rt.routes[vni][dest]) == 0 {
		delete(rt.routes[vni], dest)
	}

	if len(rt.routes[vni]) == 0 {
		delete(rt.routes, vni)
	}

	return nil, left
}

func (rt *threadUnsafeRouteTable) AddNextHop(vni VNI, dest Destination, nh NextHop) error {

	// TODO Performance: reused found map pointers
	if _, exists := rt.routes[vni]; !exists {
		rt.routes[vni] = make(map[Destination]map[NextHop]bool)
	}

	if _, exists := rt.routes[vni][dest]; !exists {
		rt.routes[vni][dest] = make(map[NextHop]bool)
	}

	if _, exists := rt.routes[vni][dest][nh]; exists {
		return fmt.Errorf("Nexthop already exists")
	}

	rt.routes[vni][dest][nh] = true

	return nil
}
