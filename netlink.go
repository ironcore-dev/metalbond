// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package metalbond

import (
	"fmt"
	"net"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
)

const METALBOND_RT_PROTO netlink.RouteProtocol = 254

type NetlinkClient struct {
	config     NetlinkClientConfig
	tunDevice  netlink.Link
	mtx        sync.Mutex
	routeTable routeTable
	mbp        *metalBondPeer
}

type NetlinkClientConfig struct {
	VNITableMap   map[VNI]int
	LinkName      string
	IPv4Only      bool
	PreferNetwork *net.IPNet
}

func NewNetlinkClient(config NetlinkClientConfig) (*NetlinkClient, error) {
	link, err := netlink.LinkByName(config.LinkName)
	if err != nil {
		return nil, fmt.Errorf("Cannot find tun device '%s': %v", config.LinkName, err)
	}

	client := &NetlinkClient{
		config:     config,
		tunDevice:  link,
		routeTable: newRouteTable(),
		mbp:        &metalBondPeer{},
	}

	// Start a goroutine to periodically clean up stale routes
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			<-ticker.C
			client.cleanupStaleRoutes()
		}
	}()

	return client, nil
}

func (c *NetlinkClient) cleanupStaleRoutes() {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	for _, table := range c.config.VNITableMap {
		filter := &netlink.Route{Table: table, Protocol: METALBOND_RT_PROTO}
		routes, err := netlink.RouteListFiltered(netlink.FAMILY_ALL, filter, netlink.RT_FILTER_TABLE|netlink.RT_FILTER_PROTOCOL)
		if err != nil {
			log.Warnf("Cannot list routes for table %d: %v", table, err)
			continue
		}
		for _, route := range routes {
			if !c.isRouteInRouteTable(route) {
				if err := netlink.RouteDel(&route); err != nil {
					log.Warnf("Failed to delete stale route %s from table %d: %v", route.Dst, table, err)
				} else {
					log.Infof("Deleted stale route %s from table %d", route.Dst, table)
				}
			}
		}
	}
}

func (c *NetlinkClient) isRouteInRouteTable(route netlink.Route) bool {
	vnis := c.routeTable.GetVNIs()
	for _, vni := range vnis {
		destinations := c.routeTable.GetDestinationsByVNI(vni)
		for dest := range destinations {
			if dest.Prefix.String() == route.Dst.String() {
				return true
			}
		}
	}
	return false
}

func (c *NetlinkClient) AddRoute(vni VNI, dest Destination, hop NextHop) error {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	if c.config.IPv4Only && dest.IPVersion != IPV4 {
		log.Infof("Received non-IPv4 route will not be installed in kernel route table (IPv4-only mode)")
		return nil
	}

	table, exists := c.config.VNITableMap[vni]
	if !exists {
		return fmt.Errorf("No route table ID known for given VNI")
	}

	_, dst, err := net.ParseCIDR(dest.Prefix.String())
	if err != nil {
		return fmt.Errorf("cannot parse destination prefix: %v", err)
	}

	err = c.routeTable.AddNextHop(vni, dest, hop, c.mbp)
	if err != nil {
		return fmt.Errorf("cannot add route to internal table vni: %d dest: %s hop: %s error: %v", vni, dest, hop, err)
	}

	route := &netlink.Route{
		Dst:      dst,
		Table:    table,
		Protocol: METALBOND_RT_PROTO,
	} // by default, the route is already installed into the kernel table without explicite specification

	multiPath := []*netlink.NexthopInfo{}
	for _, nextHop := range c.routeTable.GetNextHopsByDestination(vni, dest) {
		nexthopInfo := c.createNexthopInfo(nextHop)
		multiPath = append(multiPath, nexthopInfo)
	}

	route.MultiPath = multiPath
	if err := netlink.RouteReplace(route); err != nil {
		return fmt.Errorf("cannot replace ecmp route to %s (table %d) to kernel: %v", dest, table, err)
	}

	return nil
}

func (c *NetlinkClient) RemoveRoute(vni VNI, dest Destination, hop NextHop) error {
	c.mtx.Lock()
	defer c.mtx.Unlock()

	if c.config.IPv4Only && dest.IPVersion != IPV4 {
		return nil
	}

	table, exists := c.config.VNITableMap[vni]
	if !exists {
		return fmt.Errorf("No route table ID known for given VNI")
	}

	_, dst, err := net.ParseCIDR(dest.Prefix.String())
	if err != nil {
		return fmt.Errorf("cannot parse destination prefix: %v", err)
	}

	route := &netlink.Route{
		Dst:      dst,
		Table:    table,
		Protocol: METALBOND_RT_PROTO,
	} // by default, the route is already installed into the kernel table without explicite specification

	err, _ = c.routeTable.RemoveNextHop(vni, dest, hop, c.mbp)
	if err != nil {
		return fmt.Errorf("cannot add route to internal table vni: %d dest: %s hop: %s error: %v", vni, dest, hop, err)
	}

	multiPath := []*netlink.NexthopInfo{}
	for _, nextHop := range c.routeTable.GetNextHopsByDestination(vni, dest) {
		nexthopInfo := c.createNexthopInfo(nextHop)
		multiPath = append(multiPath, nexthopInfo)
	}

	if len(multiPath) == 0 {
		route.LinkIndex = c.tunDevice.Attrs().Index
		if err := netlink.RouteDel(route); err != nil {
			return fmt.Errorf("cannot remove route to %s (table %d) from kernel: %v", dest, table, err)
		}
	} else {
		route.MultiPath = multiPath
		if err := netlink.RouteReplace(route); err != nil {
			return fmt.Errorf("cannot replace ecmp route to %s (table %d) to kernel: %v", dest, table, err)
		}
	}

	return nil
}

func (c *NetlinkClient) createNexthopInfo(nextHop NextHop) *netlink.NexthopInfo {
	dst := net.ParseIP(nextHop.TargetAddress.String())
	encap := netlink.IP6tnlEncap{
		Dst: dst,
		Src: net.ParseIP("::"), // what source ip to put here? Metalbond object, m, does not contain this info yet.
	}
	nexthopInfo := &netlink.NexthopInfo{
		LinkIndex: c.tunDevice.Attrs().Index,
		Encap:     &encap,
		Hops:      0,
	}

	if c.config.PreferNetwork != nil && c.config.PreferNetwork.Contains(dst) {
		nexthopInfo.Hops = 99
	}

	return nexthopInfo
}
