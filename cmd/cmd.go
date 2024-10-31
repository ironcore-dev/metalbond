// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"github.com/vishvananda/netlink"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"time"

	"net/netip"

	"github.com/alecthomas/kong"
	"github.com/ironcore-dev/metalbond"
	"github.com/ironcore-dev/metalbond/pb"
	log "github.com/sirupsen/logrus"
)

var CLI struct {
	Server struct {
		Listen                   string `help:"listen address. e.g. [::]:4711"`
		Verbose                  bool   `help:"Enable debug logging" short:"v"`
		Keepalive                uint32 `help:"Keepalive Interval"`
		Http                     string `help:"HTTP Server listen address. e.g. [::]:4712"`
		TxChanCapacity           int    `help:"Specifies the maximum number of outgoing messages that can be queued before blocking; controls the buffer size for outgoing messages."`
		RxChanEventCapacity      int    `help:"Sets the buffer size for receiving HELLO and KEEPALIVE events; limits the number of concurrent event messages that can be processed."`
		RxChanDataUpdateCapacity int    `help:"Defines the capacity for subscription and update messages (e.g., SUBSCRIBE, UNSUBSCRIBE, UPDATE); manages the message queue for data updates and subscriptions."`
	} `cmd:"" help:"Run MetalBond Server"`

	Client struct {
		Server                   []string `help:"Server address. You may define multiple servers."`
		Subscribe                []uint32 `help:"Subscribe to VNIs"`
		Announce                 []string `help:"Announce Prefixes in VNIs (e.g. 23#10.0.23.0/24#2001:db8::1#[STD|LB|NAT]#[FROM#TO]"`
		Verbose                  bool     `help:"Enable debug logging" short:"v"`
		InstallRoutes            []string `help:"install routes via netlink. VNI to route table mapping (e.g. 23#100 installs routes of VNI 23 to route table 100)"`
		Tun                      string   `help:"ip6tnl tun device name"`
		IPv4only                 bool     `help:"Receive only IPv4 routes" name:"ipv4-only"`
		Keepalive                uint32   `help:"Keepalive Interval"`
		Http                     string   `help:"HTTP Server listen address. e.g. [::]:4712"`
		PreferNetwork            string   `help:"Prefer network routes (e.g. 2001:db8::1/52)"`
		RtProto                  int      `help:"Metalbond netlink.RouteProtocol, allows having multiple client populating the route table"`
		TxChanCapacity           int      `help:"Specifies the maximum number of outgoing messages that can be queued before blocking; controls the buffer size for outgoing messages."`
		RxChanEventCapacity      int      `help:"Sets the buffer size for receiving HELLO and KEEPALIVE events; limits the number of concurrent event messages that can be processed."`
		RxChanDataUpdateCapacity int      `help:"Defines the capacity for subscription and update messages (e.g., SUBSCRIBE, UNSUBSCRIBE, UPDATE); manages the message queue for data updates and subscriptions."`
	} `cmd:"" help:"Run MetalBond Client"`
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

		txChanCapacity := 2048
		if CLI.Server.TxChanCapacity > 0 {
			txChanCapacity = CLI.Server.TxChanCapacity
		}

		rxChanEventCapacity := 10
		if CLI.Server.RxChanEventCapacity > 0 {
			rxChanEventCapacity = CLI.Server.RxChanEventCapacity
		}

		rxChanDataUpdateCapacity := 100
		if CLI.Server.RxChanEventCapacity > 0 {
			rxChanDataUpdateCapacity = CLI.Server.RxChanDataUpdateCapacity
		}

		if err := m.StartServer(CLI.Server.Listen, txChanCapacity, rxChanEventCapacity, rxChanDataUpdateCapacity); err != nil {
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

			var preferNetwork *net.IPNet
			if len(CLI.Client.PreferNetwork) > 0 {
				_, preferNetwork, err = net.ParseCIDR(CLI.Client.PreferNetwork)
				if err != nil {
					log.Fatalf("invalid PreferNetwork address: %s - %v", CLI.Client.PreferNetwork, err)
				}
			}

			// rt proto defaults to 254
			rtProto := 254
			if CLI.Client.RtProto > 0 && CLI.Client.RtProto != rtProto {
				rtProto = CLI.Client.RtProto
			}

			client, err = metalbond.NewNetlinkClient(metalbond.NetlinkClientConfig{
				VNITableMap:   vnitablemap,
				LinkName:      CLI.Client.Tun,
				IPv4Only:      CLI.Client.IPv4only,
				PreferNetwork: preferNetwork,
			}, netlink.RouteProtocol(rtProto))

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

		txChanCapacity := 100
		if CLI.Server.TxChanCapacity > 0 {
			txChanCapacity = CLI.Server.TxChanCapacity
		}

		rxChanEventCapacity := 10
		if CLI.Server.RxChanEventCapacity > 0 {
			rxChanEventCapacity = CLI.Server.RxChanEventCapacity
		}

		rxChanDataUpdateCapacity := 50
		if CLI.Server.RxChanEventCapacity > 0 {
			rxChanDataUpdateCapacity = CLI.Server.RxChanDataUpdateCapacity
		}

		for _, server := range CLI.Client.Server {
			if err := m.AddPeer(server, "", txChanCapacity, rxChanEventCapacity, rxChanDataUpdateCapacity); err != nil {
				panic(fmt.Errorf("failed to add server: %v", err))
			}
		}

		// Wait for first peer to connect
		deadline := time.Now().Add(10 * time.Second)
		for {
			connected := false
			for _, server := range CLI.Client.Server {
				state, err := m.PeerState(server)
				if err == nil && state == metalbond.ESTABLISHED {
					connected = true
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
				log.Errorf("Subscription failed: %v", err)
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

		m.Shutdown()

	default:
		log.Errorf("Error: %v", ctx.Command())
	}
}
