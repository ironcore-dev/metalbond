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

package main

import (
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"time"

	"net/netip"

	"github.com/alecthomas/kong"
	"github.com/onmetal/metalbond"
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
		Announce      []string `help:"Announce Prefixes in VNIs (e.g. 23#10.0.23.0/24#2001:db8::1)"`
		Verbose       bool     `help:"Enable debug logging" short:"v"`
		InstallRoutes []string `help:"install routes via netlink. VNI to route table mapping (e.g. 23#100 installs routes of VNI 23 to route table 100)"`
		Tun           string   `help:"ip6tnl tun device name"`
		RouteTable    int      `help:"install routes into a specified table (e.g. when routes should be installed into a VRF)"`
		Keepalive     uint32   `help:"Keepalive Interval"`
		Http          string   `help:"HTTP Server listen address. e.g. [::]:4712"`
	} `cmd:"" help:"Run MetalBond Client"`
}

func main() {
	log.Infof("MetalBond %s", metalbond.METALBOND_VERSION)

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

		config := metalbond.MetalBondConfig{
			KeepaliveInterval: CLI.Server.Keepalive,
		}

		client := metalbond.NewDummyClient()
		m := metalbond.NewMetalBond(config, client)
		if len(CLI.Server.Http) > 0 {
			m.StartHTTPServer(CLI.Server.Http)
		}

		m.StartServer(CLI.Server.Listen)

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

		config := metalbond.MetalBondConfig{
			KeepaliveInterval: CLI.Client.Keepalive,
		}

		var client metalbond.MetalBondClient
		if len(CLI.Client.InstallRoutes) > 0 {
			vnitablemap := map[metalbond.VNI]int{}
			for _, mapping := range CLI.Client.InstallRoutes {
				parts := strings.Split(mapping, "#")
				if len(parts) != 2 {
					log.Fatalf("malformed VNI Table mapping: %s", mapping)
				}

				vni, err := strconv.ParseInt(parts[0], 10, 24)
				if err != nil {
					log.Fatalf("cannot parse VNI: %s", parts[0])
				}

				table, err := strconv.ParseInt(parts[1], 10, 24)
				if err != nil {
					log.Fatalf("cannot parse table: %s", parts[1])
				}

				vnitablemap[metalbond.VNI(vni)] = int(table)
			}

			log.Infof("VNI to Route Table mapping: %v", vnitablemap)

			client, err = metalbond.NewNetlinkClient(metalbond.NetlinkClientConfig{
				VNITableMap: vnitablemap,
				LinkName:    CLI.Client.Tun,
			})
			if err != nil {
				log.Fatalf("Cannot create MetalBond Client: %v", err)
			}
		} else {
			client = metalbond.NewDummyClient()
		}

		m := metalbond.NewMetalBond(config, client)
		if len(CLI.Client.Http) > 0 {
			m.StartHTTPServer(CLI.Client.Http)
		}

		for _, server := range CLI.Client.Server {
			m.AddPeer(server)
		}

		for _, subscription := range CLI.Client.Subscribe {
			err := m.Subscribe(metalbond.VNI(subscription))
			if err != nil {
				log.Fatalf("Subscription failed: %v", err)
			}
		}

		for _, announcement := range CLI.Client.Announce {
			parts := strings.Split(announcement, "#")
			if len(parts) != 3 {
				log.Fatalf("malformed announcement: %s", announcement)
			}

			vni, err := strconv.ParseInt(parts[0], 10, 24)
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
				NAT:           false,
			}

			m.AnnounceRoute(metalbond.VNI(vni), dest, hop)
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
