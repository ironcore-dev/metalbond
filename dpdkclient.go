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
	"net"
	"time"

	"context"
	"io"

	dpdkproto "github.com/onmetal/net-dpservice-go/proto"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type DPDKClient struct {
	config DPDKClientConfig
	// tunDevice netlink.Link
	// mtx       sync.Mutex
	// gRPCClient dpdkproto.DPDKonmetalClient
}

type DPDKClientConfig struct {
	// VNITableMap map[VNI]int
	// LinkName    string
	DPDKServerAddress string
	IPv4Only          bool
}

func NewDPDKClient(config DPDKClientConfig) (*DPDKClient, error) {
	// TODO: Remove all routes from route tables defined in config.VNITableMap with Protocol = METALBOND_RT_PROTO
	// to clean up old, stale routes installed by a prior metalbond client instance

	return &DPDKClient{
		config: config,
	}, nil
}

func (c *DPDKClient) AddRoute(vni VNI, dest Destination, hop NextHop) error {

	client, closer, err := c.getDpClient(c.config)
	if err != nil {
		return fmt.Errorf("cannot get dp client: %v", err)
	}

	defer closer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if c.config.IPv4Only && dest.IPVersion != IPV4 {
		log.Infof("Received non-IPv4 route will not be installed in kernel route table (IPv4-only mode)")
		return nil
	}

	_, dst, err := net.ParseCIDR(dest.Prefix.String())
	if err != nil {
		return fmt.Errorf("cannot parse destination prefix: %v", err)
	}

	prefix := &dpdkproto.Prefix{
		PrefixLength: uint32(dest.Prefix.Bits()),
	}

	prefix.IpVersion = dpdkproto.IPVersion_IPv4 //only ipv4 in overlay is supported so far
	prefix.Address = []byte(dst.IP.String())

	req := &dpdkproto.VNIRouteMsg{
		Vni: &dpdkproto.VNIMsg{Vni: uint32(vni)},
		Route: &dpdkproto.Route{
			IpVersion:      dpdkproto.IPVersion_IPv6, //only ipv4 in overlay is supported so far
			Weight:         100,                      // this field is ignored in dp-service
			Prefix:         prefix,
			NexthopVNI:     uint32(vni),
			NexthopAddress: []byte(hop.TargetAddress.String()),
		},
	}

	_, err = client.AddRoute(ctx, req)
	if err != nil {
		return fmt.Errorf("cannot add route to dpdk service: %v", err)
	}
	return nil
}

func (c *DPDKClient) RemoveRoute(vni VNI, dest Destination, hop NextHop) error {
	client, closer, err := c.getDpClient(c.config)
	if err != nil {
		return fmt.Errorf("cannot get dp client: %v", err)
	}

	defer closer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if c.config.IPv4Only && dest.IPVersion != IPV4 {
		log.Infof("Received non-IPv4 route will not be installed in kernel route table (IPv4-only mode)")
		return nil
	}

	_, dst, err := net.ParseCIDR(dest.Prefix.String())
	if err != nil {
		return fmt.Errorf("cannot parse destination prefix: %v", err)
	}

	prefix := &dpdkproto.Prefix{
		PrefixLength: uint32(dest.Prefix.Bits()),
	}

	prefix.IpVersion = dpdkproto.IPVersion_IPv4 //only ipv4 in overlay is supported so far
	prefix.Address = []byte(dst.IP.String())

	req := &dpdkproto.VNIRouteMsg{
		Vni: &dpdkproto.VNIMsg{Vni: uint32(vni)},
		Route: &dpdkproto.Route{
			IpVersion:      dpdkproto.IPVersion_IPv6, //only ipv4 in overlay is supported so far
			Weight:         100,                      // this field is ignored in dp-service
			Prefix:         prefix,
			NexthopVNI:     uint32(vni),
			NexthopAddress: []byte(hop.TargetAddress.String()),
		},
	}

	_, err = client.DeleteRoute(ctx, req)
	if err != nil {
		return fmt.Errorf("cannot remove route from the dpdk service: %v", err)
	}
	return nil

}

// think of this to be a member of DPDKClient without always creating/closing
func (c *DPDKClient) getDpClient(config DPDKClientConfig) (dpdkproto.DPDKonmetalClient, io.Closer, error) {

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, config.DPDKServerAddress, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		return nil, nil, fmt.Errorf("cannot successfully connect to gRPC server: %v", err)
	}
	client := dpdkproto.NewDPDKonmetalClient(conn)

	return client, conn, nil
}
