// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"time"

	"net/netip"

	"github.com/alecthomas/kong"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcap"
	"github.com/ironcore-dev/metalbond"
	"github.com/ironcore-dev/metalbond/pb"
	log "github.com/sirupsen/logrus"
)

var CLI struct {
	Server struct {
		Listen    string `help:"listen address. e.g. [::]:4711"`
		Verbose   bool   `help:"Enable debug logging" short:"v"`
		Keepalive uint32 `help:"Keepalive Interval"`
		Http      string `help:"HTTP Server listen address. e.g. [::]:4712"`
	} `cmd:"" help:"Run MetalBond Server"`

	Client struct {
		Server        []string `help:"Server address. You may define multiple servers."`
		Subscribe     []uint32 `help:"Subscribe to VNIs"`
		Announce      []string `help:"Announce Prefixes in VNIs (e.g. 23#10.0.23.0/24#2001:db8::1#[STD|LB|NAT]#[FROM#TO]"`
		Verbose       bool     `help:"Enable debug logging" short:"v"`
		InstallRoutes []string `help:"install routes via netlink. VNI to route table mapping (e.g. 23#100 installs routes of VNI 23 to route table 100)"`
		Tun           string   `help:"ip6tnl tun device name"`
		IPv4only      bool     `help:"Receive only IPv4 routes" name:"ipv4-only"`
		Keepalive     uint32   `help:"Keepalive Interval"`
		Http          string   `help:"HTTP Server listen address. e.g. [::]:4712"`
		ARPSpoof      struct {
			Interface string `help:"Network interface for ARP spoofing (e.g., eth0)"`
			IPPrefix  string `help:"IP prefix to spoof ARP responses for (e.g., 192.168.1.0/24)"`
		} `embed:"" prefix:"arp-spoof-"`
	} `cmd:"" help:"Run MetalBond Client"`
}

// ARPSpoofer holds the state for ARP spoofing
type ARPSpoofer struct {
	iface     string
	handle    *pcap.Handle
	mac       net.HardwareAddr
	ipNetwork *net.IPNet
	stopChan  chan struct{}
}

// NewARPSpoofer creates a new ARP spoofer for the given interface and IP prefix
func NewARPSpoofer(iface, ipPrefix string) (*ARPSpoofer, error) {
	// Parse IP prefix
	_, ipNet, err := net.ParseCIDR(ipPrefix)
	if err != nil {
		return nil, fmt.Errorf("invalid IP prefix: %v", err)
	}

	// Get interface info
	netIface, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, fmt.Errorf("interface not found: %v", err)
	}

	// Open pcap handle for the interface
	handle, err := pcap.OpenLive(iface, 65536, true, pcap.BlockForever)
	if err != nil {
		return nil, fmt.Errorf("failed to open interface: %v", err)
	}

	// Set BPF filter to only capture ARP packets
	if err := handle.SetBPFFilter("arp"); err != nil {
		handle.Close()
		return nil, fmt.Errorf("failed to set BPF filter: %v", err)
	}

	return &ARPSpoofer{
		iface:     iface,
		handle:    handle,
		mac:       netIface.HardwareAddr,
		ipNetwork: ipNet,
		stopChan:  make(chan struct{}),
	}, nil
}

// Start begins the ARP spoofing process
func (a *ARPSpoofer) Start() {
	log.Infof("Starting ARP spoofing on interface %s for IP range %s", a.iface, a.ipNetwork.String())
	log.Infof("Using MAC address: %s", a.mac.String())

	go func() {
		packetSource := gopacket.NewPacketSource(a.handle, a.handle.LinkType())
		for {
			select {
			case <-a.stopChan:
				return
			case packet := <-packetSource.Packets():
				a.handlePacket(packet)
			}
		}
	}()
}

func (a *ARPSpoofer) handlePacket(packet gopacket.Packet) {
	arpLayer := packet.Layer(layers.LayerTypeARP)
	if arpLayer == nil {
		return
	}

	arp := arpLayer.(*layers.ARP)
	if arp.Operation != layers.ARPRequest {
		return
	}

	targetIP := net.IP(arp.DstProtAddress)

	if !a.ipNetwork.Contains(targetIP) {
		return
	}

	eth := layers.Ethernet{
		SrcMAC:       a.mac,
		DstMAC:       net.HardwareAddr(arp.SourceHwAddress),
		EthernetType: layers.EthernetTypeARP,
	}

	arpReply := layers.ARP{
		AddrType:          arp.AddrType,
		Protocol:          arp.Protocol,
		HwAddressSize:     arp.HwAddressSize,
		ProtAddressSize:   arp.ProtAddressSize,
		Operation:         layers.ARPReply,
		SourceHwAddress:   a.mac,
		SourceProtAddress: arp.DstProtAddress,
		DstHwAddress:      arp.SourceHwAddress,
		DstProtAddress:    arp.SourceProtAddress,
	}

	buffer := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{
		FixLengths:       true,
		ComputeChecksums: true,
	}

	if err := gopacket.SerializeLayers(buffer, opts, &eth, &arpReply); err != nil {
		log.Errorf("Failed to serialize ARP reply: %v", err)
		return
	}

	if err := a.handle.WritePacketData(buffer.Bytes()); err != nil {
		log.Errorf("Failed to send ARP reply: %v", err)
		return
	}

	log.Debugf("Sent ARP reply for %s to %s",
		net.IP(arp.DstProtAddress).String(),
		net.IP(arp.SourceProtAddress).String())
}

func (a *ARPSpoofer) Stop() {
	close(a.stopChan)
	a.handle.Close()
	log.Infof("ARP spoofing stopped")
}

func main() {
	if metalbond.METALBOND_VERSION == "" {
		metalbond.METALBOND_VERSION = "development" // Fallback for when version is not set
	}
	log.Infof("MetalBond Version: %s", metalbond.METALBOND_VERSION)

	go func() {
		for {
			log.Debugf("Active Go Routines: %d", runtime.NumGoroutine())
			time.Sleep(time.Duration(10 * time.Second))
		}
	}()

	ctx := kong.Parse(&CLI)
	switch ctx.Command() {
	case "server":
		if CLI.Server.Verbose {
			log.SetLevel(log.DebugLevel)
		}

		config := metalbond.Config{
			KeepaliveInterval: CLI.Server.Keepalive,
		}

		client := metalbond.NewDummyClient()
		m := metalbond.NewMetalBond(config, client)
		if len(CLI.Server.Http) > 0 {
			if err := m.StartHTTPServer(CLI.Server.Http); err != nil {
				panic(fmt.Errorf("failed to start http server: %v", err))
			}
		}

		if err := m.StartServer(CLI.Server.Listen); err != nil {
			panic(fmt.Errorf("failed to start server: %v", err))
		}

		// Wait for SIGINTs
		cint := make(chan os.Signal, 1)
		signal.Notify(cint, os.Interrupt)
		<-cint

		m.Shutdown()

	case "client":
		log.Infof("Client")
		log.Infof("  servers: %v", CLI.Client.Server)
		var err error

		if CLI.Client.Verbose {
			log.SetLevel(log.DebugLevel)
		}

		config := metalbond.Config{
			KeepaliveInterval: CLI.Client.Keepalive,
		}

		var client metalbond.Client
		if len(CLI.Client.InstallRoutes) > 0 {
			vnitablemap := map[metalbond.VNI]int{}
			for _, mapping := range CLI.Client.InstallRoutes {
				parts := strings.Split(mapping, "#")
				if len(parts) != 2 {
					log.Fatalf("malformed VNI Table mapping: %s", mapping)
				}

				vni, err := strconv.ParseUint(parts[0], 10, 24)
				if err != nil {
					log.Fatalf("cannot parse VNI: %s", parts[0])
				}

				table, err := strconv.ParseUint(parts[1], 10, 24)
				if err != nil {
					log.Fatalf("cannot parse table: %s", parts[1])
				}

				vnitablemap[metalbond.VNI(vni)] = int(table)
			}

			log.Infof("VNI to Route Table mapping: %v", vnitablemap)

			client, err = metalbond.NewNetlinkClient(metalbond.NetlinkClientConfig{
				VNITableMap: vnitablemap,
				LinkName:    CLI.Client.Tun,
				IPv4Only:    CLI.Client.IPv4only,
			})
			if err != nil {
				log.Fatalf("Cannot create MetalBond Client: %v", err)
			}
		} else {
			client = metalbond.NewDummyClient()
		}

		m := metalbond.NewMetalBond(config, client)
		if len(CLI.Client.Http) > 0 {
			if err := m.StartHTTPServer(CLI.Client.Http); err != nil {
				panic(fmt.Errorf("failed to start http server: %v", err))
			}
		}

		// Start ARP spoofing if both interface and IP prefix are provided
		var arpSpoofer *ARPSpoofer
		if CLI.Client.ARPSpoof.Interface != "" && CLI.Client.ARPSpoof.IPPrefix != "" {
			log.Infof("Initializing ARP spoofing on interface %s for IP range %s",
				CLI.Client.ARPSpoof.Interface, CLI.Client.ARPSpoof.IPPrefix)

			arpSpoofer, err = NewARPSpoofer(CLI.Client.ARPSpoof.Interface, CLI.Client.ARPSpoof.IPPrefix)
			if err != nil {
				log.Warnf("Failed to initialize ARP spoofing: %v", err)
			} else {
				arpSpoofer.Start()
			}
		}

		for _, server := range CLI.Client.Server {
			if err := m.AddPeer(server, ""); err != nil {
				panic(fmt.Errorf("failed to add server: %v", err))
			}
		}

		// Wait for all peers to connect
		deadline := time.Now().Add(10 * time.Second)
		for {
			connected := true
			for _, server := range CLI.Client.Server {
				state, err := m.PeerState(server)
				if err != nil || state != metalbond.ESTABLISHED {
					connected = false
					break
				}
			}
			if connected {
				break
			}
			if time.Now().After(deadline) {
				panic(errors.New("timeout waiting to connect"))
			}
			time.Sleep(1 * time.Second)
		}

		for _, subscription := range CLI.Client.Subscribe {
			err := m.Subscribe(metalbond.VNI(subscription))
			if err != nil {
				log.Fatalf("Subscription failed: %v", err)
			}
		}

		for _, announcement := range CLI.Client.Announce {
			parts := strings.Split(announcement, "#")
			routeType := pb.NextHopType_STANDARD
			if len(parts) != 4 && len(parts) != 3 && len(parts) != 6 {
				log.Fatalf("malformed announcement: %s expected format vni#prefix#destHop[#routeType][#fromPort#toPort] routeType can be STD,LB or NAT", announcement)
			}

			if len(parts) > 3 {
				routeType = pb.ConvertCmdLineStrToEnumValue(parts[3])
			}

			vni, err := strconv.ParseUint(parts[0], 10, 24)
			if err != nil {
				log.Fatalf("invalid VNI: %s", parts[1])
			}

			prefix, err := netip.ParsePrefix(parts[1])
			if err != nil {
				log.Fatalf("invalid prefix: %s", parts[1])
			}

			var ipversion metalbond.IPVersion
			if prefix.Addr().Is4() {
				ipversion = metalbond.IPV4
			} else {
				ipversion = metalbond.IPV6
			}

			dest := metalbond.Destination{
				IPVersion: ipversion,
				Prefix:    prefix,
			}

			hopIP, err := netip.ParseAddr(parts[2])
			if err != nil {
				log.Fatalf("invalid nexthop address: %s - %v", parts[2], err)
			}

			hop := metalbond.NextHop{
				TargetAddress: hopIP,
				TargetVNI:     0,
				Type:          routeType,
			}
			if routeType == pb.NextHopType_NAT {
				if len(parts) <= 4 {
					log.Fatalf("malformed announcement for NAT: %s expected format vni#prefix#destHop[#routeType][#fromPort#toPort] routeType can be STD,LB or NAT", announcement)
				}
				from, err := strconv.ParseInt(parts[4], 10, 16)
				if err != nil {
					log.Fatalf("invalid NAT from: %s", parts[4])
				}
				to, err := strconv.ParseInt(parts[5], 10, 16)
				if err != nil {
					log.Fatalf("invalid NAT from: %s", parts[5])
				}
				hop.NATPortRangeFrom = uint16(from)
				hop.NATPortRangeTo = uint16(to)
			}

			if err := m.AnnounceRoute(metalbond.VNI(vni), dest, hop); err != nil {
				log.Fatalf("failed to announce route: %v", err)
			}
		}

		// Wait for SIGINTs
		cint := make(chan os.Signal, 1)
		signal.Notify(cint, os.Interrupt)
		<-cint

		// Stop ARP spoofing if it was started
		if arpSpoofer != nil {
			arpSpoofer.Stop()
		}

		m.Shutdown()

	default:
		log.Errorf("Error: %v", ctx.Command())
	}
}
