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

type routeKey struct {
	vni  VNI
	dest Destination
}

type routeState struct {
	installed NextHop
	hops      map[NextHop]struct{}
}

type NetlinkClient struct {
	config    NetlinkClientConfig
	tunDevice netlink.Link
	mtx       sync.Mutex
	routes    map[routeKey]*routeState
}

type NetlinkClientConfig struct {
	VNITableMap map[VNI]int
	LinkName    string
	IPv4Only    bool
}

func NewNetlinkClient(config NetlinkClientConfig) (*NetlinkClient, error) {
	link, err := netlink.LinkByName(config.LinkName)
	if err != nil {
		return nil, fmt.Errorf("cannot find tun device '%s': %v", config.LinkName, err)
	}

	// TODO: Remove all routes from route tables defined in config.VNITableMap with Protocol = METALBOND_RT_PROTO
	// to clean up old, stale routes installed by a prior metalbond client instance

	return &NetlinkClient{
		config:    config,
		tunDevice: link,
		routes:    make(map[routeKey]*routeState),
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
		return fmt.Errorf("no route table ID known for given VNI")
	}

	key := routeKey{vni: vni, dest: dest}
	state, exists := c.routes[key]
	if !exists {
		state = &routeState{hops: make(map[NextHop]struct{})}
		c.routes[key] = state
	}
	state.hops[hop] = struct{}{}

	if len(state.hops) > 1 {
		return nil
	}

	err := c.installRoute(dest, hop, table)
	if err != nil {
		return err
	}
	state.installed = hop
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
		return fmt.Errorf("no route table ID known for given VNI")
	}

	key := routeKey{vni: vni, dest: dest}
	state, exists := c.routes[key]
	if !exists {
		return fmt.Errorf("no routes known for VNI %d dest %s", vni, dest)
	}

	delete(state.hops, hop)

	if state.installed != hop {
		if len(state.hops) == 0 {
			delete(c.routes, key)
		}
		return nil
	}

	if len(state.hops) == 0 {
		delete(c.routes, key)
		err := c.uninstallRoute(dest, hop, table)
		if err != nil {
			return fmt.Errorf("cannot remove route to %s from kernel: %v", dest, err)
		}
		return nil
	}

	var replacement NextHop
	for nh := range state.hops {
		replacement = nh
		break
	}

	err := c.replaceRoute(dest, replacement, table)
	if err != nil {
		return fmt.Errorf("cannot install replacement route for %s: %v", dest, err)
	}
	state.installed = replacement
	return nil
}

func (c *NetlinkClient) installRoute(dest Destination, hop NextHop, table int) error {
	_, dst, err := net.ParseCIDR(dest.Prefix.String())
	if err != nil {
		return fmt.Errorf("cannot parse destination prefix: %v", err)
	}

	encap := netlink.IP6tnlEncap{
		Dst: net.ParseIP(hop.TargetAddress.String()),
		Src: net.ParseIP("::"),
	}

	route := &netlink.Route{
		LinkIndex: c.tunDevice.Attrs().Index,
		Dst:       dst,
		Encap:     &encap,
		Table:     table,
		Protocol:  METALBOND_RT_PROTO,
	}

	if err := netlink.RouteAdd(route); err != nil {
		return fmt.Errorf("cannot add route to %s (table %d) to kernel: %v", dest, table, err)
	}

	return nil
}

func (c *NetlinkClient) uninstallRoute(dest Destination, hop NextHop, table int) error {
	_, dst, err := net.ParseCIDR(dest.Prefix.String())
	if err != nil {
		return fmt.Errorf("cannot parse destination prefix: %v", err)
	}

	encap := netlink.IP6tnlEncap{
		Dst: net.ParseIP(hop.TargetAddress.String()),
		Src: net.ParseIP("::"),
	}

	route := &netlink.Route{
		LinkIndex: c.tunDevice.Attrs().Index,
		Dst:       dst,
		Encap:     &encap,
		Table:     table,
		Protocol:  METALBOND_RT_PROTO,
	}

	if err := netlink.RouteDel(route); err != nil {
		return fmt.Errorf("cannot remove route to %s (table %d) from kernel: %v", dest, table, err)
	}

	return nil
}

func (c *NetlinkClient) replaceRoute(dest Destination, hop NextHop, table int) error {
	_, dst, err := net.ParseCIDR(dest.Prefix.String())
	if err != nil {
		return fmt.Errorf("cannot parse destination prefix: %v", err)
	}

	encap := netlink.IP6tnlEncap{
		Dst: net.ParseIP(hop.TargetAddress.String()),
		Src: net.ParseIP("::"),
	}

	route := &netlink.Route{
		LinkIndex: c.tunDevice.Attrs().Index,
		Dst:       dst,
		Encap:     &encap,
		Table:     table,
		Protocol:  METALBOND_RT_PROTO,
	}

	if err := netlink.RouteReplace(route); err != nil {
		return fmt.Errorf("cannot replace route to %s (table %d) in kernel: %v", dest, table, err)
	}

	return nil
}
