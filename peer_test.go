// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package metalbond

import (
	"fmt"
	"net"
	"net/netip"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	log "github.com/sirupsen/logrus"
)

func peerState(mb *MetalBond, addr string) func() ConnectionState {
	return func() ConnectionState {
		mb.mtxPeers.RLock()
		peer := mb.peers[addr]
		mb.mtxPeers.RUnlock()
		if peer == nil {
			return CLOSED
		}
		return peer.GetState()
	}
}

func localAddr(mb *MetalBond, dest *string, notExpect string) func() string {
	return func() string {
		for _, peer := range mb.peers {
			addr := peer.LocalAddr()
			if addr != "" && addr != notExpect {
				*dest = addr
				return addr
			}
		}
		return ""
	}
}

type TrackingClient struct {
	mtx    sync.Mutex
	routes map[VNI]map[Destination][]NextHop
}

func NewTrackingClient() *TrackingClient {
	return &TrackingClient{
		routes: make(map[VNI]map[Destination][]NextHop),
	}
}

func (c *TrackingClient) AddRoute(vni VNI, dest Destination, nexthop NextHop) error {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.routes[vni] == nil {
		c.routes[vni] = make(map[Destination][]NextHop)
	}
	c.routes[vni][dest] = append(c.routes[vni][dest], nexthop)
	return nil
}

func (c *TrackingClient) RemoveRoute(vni VNI, dest Destination, nexthop NextHop) error {
	return nil
}

func (c *TrackingClient) HasRoute(vni VNI, dest Destination) bool {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.routes[vni] == nil {
		return false
	}
	_, exists := c.routes[vni][dest]
	return exists
}

var _ = Describe("Peer", func() {

	var (
		mbServer      *MetalBond
		serverAddress string
		client        *DummyClient
	)

	BeforeEach(func() {
		log.Info("----- START -----")
		config := Config{}
		client = NewDummyClient()
		mbServer = NewMetalBond(config, client)
		serverAddress = fmt.Sprintf("127.0.0.1:%d", getRandomTCPPort())
		err := mbServer.StartServer(serverAddress)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		mbServer.Shutdown()
	})

	It("should subscribe", func() {
		mbClient := NewMetalBond(Config{}, client)
		err := mbClient.AddPeer(serverAddress, "")
		Expect(err).NotTo(HaveOccurred())

		Eventually(peerState(mbClient, serverAddress)).Should(Equal(ESTABLISHED))
		vni := VNI(200)
		err = mbClient.Subscribe(vni)
		if err != nil {
			log.Errorf("subscribe failed: %v", err)
		}
		Expect(err).NotTo(HaveOccurred())

		vnis := mbClient.GetSubscribedVnis()
		Expect(len(vnis)).To(Equal(1))
		Expect(vnis[0]).To(Equal(vni))

		err = mbClient.Unsubscribe(vni)
		Expect(err).NotTo(HaveOccurred())

		vnis = mbClient.GetSubscribedVnis()
		Expect(len(vnis)).To(Equal(0))

		err = mbClient.RemovePeer(serverAddress)
		Expect(err).NotTo(HaveOccurred())

		mbClient.Shutdown()
	})

	It("should reset", func() {
		mbClient := NewMetalBond(Config{}, client)
		err := mbClient.AddPeer(serverAddress, "")
		Expect(err).NotTo(HaveOccurred())

		var clientAddr string
		Eventually(localAddr(mbClient, &clientAddr, "")).ShouldNot(BeEmpty())

		Eventually(peerState(mbServer, clientAddr)).Should(Equal(ESTABLISHED))

		var p *metalBondPeer
		for _, peer := range mbServer.peers {
			p = peer
			break
		}

		// Reset the peer a few times
		p.Reset()
		p.Reset()
		p.Reset()

		// expect the peer state to be closed
		Expect(p.GetState()).To(Equal(CLOSED))

		Eventually(localAddr(mbClient, &clientAddr, clientAddr)).ShouldNot(BeEmpty())

		Eventually(peerState(mbServer, clientAddr)).Should(Equal(ESTABLISHED))
	})

	It("should reconnect", func() {
		mbClient := NewMetalBond(Config{}, client)
		err := mbClient.AddPeer(serverAddress, "")
		Expect(err).NotTo(HaveOccurred())

		var clientAddr string
		Eventually(localAddr(mbClient, &clientAddr, "")).ShouldNot(BeEmpty())

		Eventually(peerState(mbServer, clientAddr)).Should(Equal(ESTABLISHED))

		var p *metalBondPeer
		for _, peer := range mbServer.peers {
			p = peer
			break
		}

		// Close the peer
		p.Close()

		// expect the peer state to be closed
		Expect(p.GetState()).To(Equal(CLOSED))

		Eventually(localAddr(mbClient, &clientAddr, clientAddr)).ShouldNot(BeEmpty())

		Eventually(peerState(mbServer, clientAddr)).Should(Equal(ESTABLISHED))
	})

	It("client timeout", func() {
		mbClient := NewMetalBond(Config{
			KeepaliveInterval: 1,
		}, client)
		err := mbClient.AddPeer(serverAddress, "")
		Expect(err).NotTo(HaveOccurred())

		var clientAddr string
		Eventually(localAddr(mbClient, &clientAddr, "")).ShouldNot(BeEmpty())

		Eventually(peerState(mbServer, clientAddr)).Should(Equal(ESTABLISHED))

		vni := VNI(200)
		err = mbClient.Subscribe(vni)
		Expect(err).NotTo(HaveOccurred())

		var p *metalBondPeer
		for _, peer := range mbClient.peers {
			p = peer
			break
		}

		err = mbClient.Unsubscribe(vni)
		Expect(err).NotTo(HaveOccurred())

		// Close the keepalive
		p.keepaliveStop <- true

		Eventually(p.GetState).Should(Equal(RETRY))

		err = mbClient.RemovePeer(serverAddress)
		Expect(err).NotTo(HaveOccurred())
	})

	It("should announce", func() {
		savedRetryMin := RetryIntervalMin
		savedRetryMax := RetryIntervalMax
		RetryIntervalMin = 0
		RetryIntervalMax = 1
		defer func() {
			RetryIntervalMin = savedRetryMin
			RetryIntervalMax = savedRetryMax
		}()

		totalClients := 50
		var wg sync.WaitGroup
		for i := 1; i < totalClients+1; i++ {
			wg.Add(1)

			go func(index int) {
				defer wg.Done()
				mbClient := NewMetalBond(Config{}, client)
				err := mbClient.AddPeer(serverAddress, "")
				Expect(err).NotTo(HaveOccurred())

				var clientAddr string
				Eventually(localAddr(mbClient, &clientAddr, "")).ShouldNot(BeEmpty())

				Eventually(peerState(mbServer, clientAddr)).Should(Equal(ESTABLISHED))

				mbServer.mtxPeers.RLock()
				p := mbServer.peers[clientAddr]
				mbServer.mtxPeers.RUnlock()

				Eventually(peerState(mbClient, serverAddress)).Should(Equal(ESTABLISHED))
				vni := VNI(index % 10)
				err = mbClient.Subscribe(vni)
				Expect(err).NotTo(HaveOccurred())

				// prepare the route
				startIP := net.ParseIP("100.64.0.0")
				ip := incrementIPv4(startIP, index)
				addr, err := netip.ParseAddr(ip.String())
				Expect(err).NotTo(HaveOccurred())
				underlayRoute, err := netip.ParseAddr(fmt.Sprintf("b198:5b10:3880:fd32:fb80:80dd:46f7:%d", index))
				Expect(err).NotTo(HaveOccurred())
				dest := Destination{
					Prefix:    netip.PrefixFrom(addr, 32),
					IPVersion: IPV4,
				}
				nextHop := NextHop{
					TargetVNI:     uint32(vni),
					TargetAddress: underlayRoute,
				}

				err = mbClient.AnnounceRoute(vni, dest, nextHop)
				Expect(err).NotTo(HaveOccurred())

				Eventually(func() bool {
					return p.receivedRoutes.NextHopExists(vni, dest, nextHop, p)
				}).Should(BeTrue())

				// Close the peer
				err = p.metalbond.RemovePeer(p.remoteAddr)
				Expect(err).NotTo(HaveOccurred())

				// expect the peer state to be closed
				Expect(p.GetState()).To(Equal(CLOSED))

				Eventually(localAddr(mbClient, &clientAddr, clientAddr)).ShouldNot(BeEmpty())

				Eventually(peerState(mbServer, clientAddr)).Should(Equal(ESTABLISHED))

				mbServer.mtxPeers.RLock()
				p = mbServer.peers[clientAddr]
				mbServer.mtxPeers.RUnlock()

				Eventually(func() bool {
					return p.receivedRoutes.NextHopExists(vni, dest, nextHop, p)
				}).Should(BeTrue())
			}(i)
		}

		wg.Wait()
	})
})

func incrementIPv4(ip net.IP, count int) net.IP {
	// Increment the IP address by the count
	for i := len(ip) - 1; i >= 0; i-- {
		octet := int(ip[i]) + (count % 256)
		count /= 256
		if octet > 255 {
			octet = 255
		}
		ip[i] = byte(octet)
		if count == 0 {
			break
		}
	}
	return ip
}

var _ = Describe("Route Filtering", func() {
	var (
		mbServer      *MetalBond
		serverAddress string
	)

	BeforeEach(func() {
		log.Info("----- START Route Filtering -----")
		config := Config{}
		mbServer = NewMetalBond(config, NewDummyClient())
		serverAddress = fmt.Sprintf("127.0.0.1:%d", getRandomTCPPort())
		err := mbServer.StartServer(serverAddress)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		mbServer.Shutdown()
	})

	It("should not install routes for destinations already announced", func() {
		vni := VNI(100)

		client1 := NewTrackingClient()
		client2 := NewTrackingClient()

		mbClient1 := NewMetalBond(Config{}, client1)
		mbClient2 := NewMetalBond(Config{}, client2)

		err := mbClient1.AddPeer(serverAddress, "")
		Expect(err).NotTo(HaveOccurred())
		err = mbClient2.AddPeer(serverAddress, "")
		Expect(err).NotTo(HaveOccurred())

		Eventually(peerState(mbClient1, serverAddress)).Should(Equal(ESTABLISHED))
		Eventually(peerState(mbClient2, serverAddress)).Should(Equal(ESTABLISHED))

		err = mbClient1.Subscribe(vni)
		Expect(err).NotTo(HaveOccurred())
		err = mbClient2.Subscribe(vni)
		Expect(err).NotTo(HaveOccurred())

		dest := Destination{
			Prefix:    netip.MustParsePrefix("0.0.0.0/0"),
			IPVersion: IPV4,
		}

		nextHop1 := NextHop{
			TargetVNI:     uint32(vni),
			TargetAddress: netip.MustParseAddr("fd00::1"),
		}
		nextHop2 := NextHop{
			TargetVNI:     uint32(vni),
			TargetAddress: netip.MustParseAddr("fd00::2"),
		}

		err = mbClient1.AnnounceRoute(vni, dest, nextHop1)
		Expect(err).NotTo(HaveOccurred())
		err = mbClient2.AnnounceRoute(vni, dest, nextHop2)
		Expect(err).NotTo(HaveOccurred())

		Consistently(func() bool {
			return client1.HasRoute(vni, dest)
		}).Should(BeFalse(), "client1 should not have installed route for destination it announces")
		Consistently(func() bool {
			return client2.HasRoute(vni, dest)
		}).Should(BeFalse(), "client2 should not have installed route for destination it announces")

		mbClient1.Shutdown()
		mbClient2.Shutdown()
	})
})
