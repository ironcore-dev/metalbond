// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package metalbond

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sync"

	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
)

const METALBOND_RT_PROTO netlink.RouteProtocol = 254

type routeKey struct {
	vni  VNI
	dest Destination
}

type NetlinkClient struct {
	config    NetlinkClientConfig
	tunDevice netlink.Link
	mtx       sync.Mutex
	routes    map[routeKey][]NextHop
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

	c := &NetlinkClient{
		config:    config,
		tunDevice: link,
		routes:    make(map[routeKey][]NextHop),
	}

	// Ensure we have no stale routes in the route tables which were configured.
	err = c.Cleanup()
	if err != nil {
		return nil, fmt.Errorf("unable to clean up stale routes: %w", err)
	}

	return c, nil
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
	current := c.routes[key]

	if slices.Contains(current, hop) {
		return nil
	}

	next := append(slices.Clone(current), hop)

	// Ensure deterministic order to make comparing route tables across machines
	// easier (e.g. via `ip route ...`).
	slices.SortFunc(next, func(a, b NextHop) int {
		return a.TargetAddress.Compare(b.TargetAddress)
	})

	err := c.replaceRoute(dest.Prefix, next, table)
	if err != nil {
		return err
	}

	c.routes[key] = next

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
	current, exists := c.routes[key]
	if !exists {
		return fmt.Errorf("no routes known for VNI %d dest %s", vni, dest)
	}
	next := slices.DeleteFunc(slices.Clone(current), func(e NextHop) bool { return e == hop })

	if len(current) == len(next) {
		// Delete was a no-op, skip the call to the kernel.
		return nil
	}

	if len(next) == 0 {
		if err := c.removeRoute(dest.Prefix, table); err != nil {
			return fmt.Errorf("cannot remove route to %s from kernel: %w", dest, err)
		}
		delete(c.routes, key)
	} else {
		if err := c.replaceRoute(dest.Prefix, next, table); err != nil {
			return fmt.Errorf("cannot install replacement route for %s: %w", dest, err)
		}
		c.routes[key] = next
	}

	return nil
}

// Cleanup retrieves all routes that have been installed into one of the
// configured tables and deletes them to prevent stale routes from lingering.
func (c *NetlinkClient) Cleanup() error {
	var errs []error
	for _, table := range c.config.VNITableMap {
		routes, err := netlink.RouteListFiltered(
			netlink.FAMILY_ALL,
			&netlink.Route{
				Table:    table,
				Protocol: METALBOND_RT_PROTO,
			},
			netlink.RT_FILTER_TABLE|netlink.RT_FILTER_PROTOCOL,
		)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		for _, r := range routes {
			err := netlink.RouteDel(&r)
			if err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errors.Join(errs...)
}

func (c *NetlinkClient) replaceRoute(dst netip.Prefix, hops []NextHop, table int) error {
	var nextHopInfos []*netlink.NexthopInfo
	for _, hop := range hops {
		nextHopInfos = append(nextHopInfos, &netlink.NexthopInfo{
			LinkIndex: c.tunDevice.Attrs().Index,
			Encap: &netlink.IP6tnlEncap{
				Dst: hop.TargetAddress.AsSlice(),
				Src: net.IPv6zero,
			},
		})
	}

	route := &netlink.Route{
		Table:    table,
		Protocol: METALBOND_RT_PROTO,
		Dst: &net.IPNet{
			IP:   dst.Addr().AsSlice(),
			Mask: net.CIDRMask(dst.Bits(), dst.Addr().BitLen()),
		},
		MultiPath: nextHopInfos,
	}

	if err := netlink.RouteReplace(route); err != nil {
		return fmt.Errorf("cannot replace route to %s (table %d) in kernel: %w", dst, table, err)
	}

	return nil
}

func (c *NetlinkClient) removeRoute(dst netip.Prefix, table int) error {
	route := &netlink.Route{
		Table:    table,
		Protocol: METALBOND_RT_PROTO,
		Dst: &net.IPNet{
			IP:   dst.Addr().AsSlice(),
			Mask: net.CIDRMask(dst.Bits(), dst.Addr().BitLen()),
		},
	}

	if err := netlink.RouteDel(route); err != nil {
		return fmt.Errorf("cannot remove route to %s (table %d) from kernel: %w", dst, table, err)
	}

	return nil
}
