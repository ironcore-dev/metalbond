// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package metalbond

import (
	"fmt"
	"net"
	"sync"

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

	// TODO: Remove all routes from route tables defined in config.VNITableMap with Protocol = METALBOND_RT_PROTO
	// to clean up old, stale routes installed by a prior metalbond client instance

	return &NetlinkClient{
		config:     config,
		tunDevice:  link,
		routeTable: newRouteTable(),
		mbp:        &metalBondPeer{},
	}, nil
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
	encap := netlink.IP6tnlEncap{
		Dst: net.ParseIP(nextHop.TargetAddress.String()),
		Src: net.ParseIP("::"), // what source ip to put here? Metalbond object, m, does not contain this info yet.
	}
	nexthopInfo := &netlink.NexthopInfo{
		LinkIndex: c.tunDevice.Attrs().Index,
		Encap:     &encap,
	}

	return nexthopInfo
}
