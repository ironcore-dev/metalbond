package metalbond

import (
	"fmt"
	"net"

	nl "github.com/vishvananda/netlink"
)

func (m *MetalBond) installRoute(dest Destination, hop NextHop) error {
	// TODO: Install route into Kernel
	// Destination: dest
	// via Underlay IP as defined in: hop
	// into Kernel route table: m.kernelRouteTableID
	// device: m.tunDevice

	_, dst, err := net.ParseCIDR(dest.Prefix.String())
	if err != nil {
		return fmt.Errorf("cannot parse destination prefix: %v", err)
	}

	encap := nl.IP6tnlEncap{
		Dst: net.ParseIP(hop.TargetAddress.String()),
		Src: net.ParseIP("::"), // what source ip to put here? Metalbond object, m, does not contain this info yet.
	}

	route := &nl.Route{
		LinkIndex: m.tunDevice.Attrs().Index,
		Dst:       dst,
		Encap:     &encap,
	} // by default, the route is already installed into the kernel table without explicite specification

	if err := nl.RouteAdd(route); err != nil {
		return fmt.Errorf("cannot add route: %v", err)
	}

	return nil
}
