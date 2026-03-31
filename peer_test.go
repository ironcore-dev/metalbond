// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package metalbond

import (
	"fmt"
	"math/rand"
	"net"
	"net/netip"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	log "github.com/sirupsen/logrus"
)

var _ = Describe("Peer", func() {

	var (
		mbServer, mbServer2           *MetalBond
		serverAddress, serverAddress2 string
		client                        *DummyClient
	)

	BeforeEach(func() {
		log.Info("----- START -----")
		config := Config{}
		client = NewDummyClient()
		mbServer = NewMetalBond(config, client)
		mbServer2 = NewMetalBond(config, client)
		randomPort := getRandomTCPPort()
		serverAddress = fmt.Sprintf("127.0.0.1:%d", randomPort)
		serverAddress2 = fmt.Sprintf("127.0.0.2:%d", randomPort)
		err := mbServer.StartServer(serverAddress)
		Expect(err).ToNot(HaveOccurred())
		err = mbServer2.StartServer(serverAddress2)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		mbServer.Shutdown()
		mbServer2.Shutdown()
	})

	It("should subscribe", Label("subscribe"), func() {
		mbClient := NewMetalBond(Config{}, client)
		localIP := net.ParseIP("127.0.0.3")
		err := mbClient.AddPeer(serverAddress, localIP.String())
		Expect(err).NotTo(HaveOccurred())

		time.Sleep(5 * time.Second)
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

	It("should reset", Label("reset"), func() {
		mbClient := NewMetalBond(Config{}, client)
		err := mbClient.AddPeer(serverAddress, "127.0.0.3")
		Expect(err).NotTo(HaveOccurred())

		clientAddr := getLocalAddr(mbClient, "")
		Expect(clientAddr).NotTo(Equal(""))

		Expect(waitForPeerState(mbServer, clientAddr, ESTABLISHED)).NotTo(BeFalse())

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

		clientAddr = getLocalAddr(mbClient, clientAddr)
		Expect(clientAddr).NotTo(Equal(""))

		// wait for the peer to be established again
		Expect(waitForPeerState(mbServer, clientAddr, ESTABLISHED)).NotTo(BeFalse())
	})

	It("should reconnect", Label("reconnect"), func() {
		mbClient := NewMetalBond(Config{}, client)
		err := mbClient.AddPeer(serverAddress, "127.0.0.3")
		Expect(err).NotTo(HaveOccurred())

		clientAddr := getLocalAddr(mbClient, "")
		Expect(clientAddr).NotTo(Equal(""))

		Expect(waitForPeerState(mbServer, clientAddr, ESTABLISHED)).NotTo(BeFalse())

		var p *metalBondPeer
		for _, peer := range mbServer.peers {
			p = peer
			break
		}

		// Close the peer
		p.Close()

		// expect the peer state to be closed
		Expect(p.GetState()).To(Equal(CLOSED))

		clientAddr = getLocalAddr(mbClient, clientAddr)
		Expect(clientAddr).NotTo(Equal(""))

		// wait for the peer to be established again
		Expect(waitForPeerState(mbServer, clientAddr, ESTABLISHED)).NotTo(BeFalse())
	})

	It("client timeout", Label("timeout"), func() {
		mbClient := NewMetalBond(Config{}, client)
		err := mbClient.AddPeer(serverAddress, "127.0.0.3")
		Expect(err).NotTo(HaveOccurred())

		clientAddr := getLocalAddr(mbClient, "")
		Expect(clientAddr).NotTo(Equal(""))

		Expect(waitForPeerState(mbServer, clientAddr, ESTABLISHED)).NotTo(BeFalse())

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

		time.Sleep(12 * time.Second)

		// expect the peer state to be closed
		Expect(p.GetState()).To(Equal(RETRY))

		err = mbClient.RemovePeer(serverAddress)
		Expect(err).NotTo(HaveOccurred())
	})

	It("should announce", Label("announce"), func() {
		totalClients := 600 // TODO: was 1000 (local test works for this large value), but it is reduced to this value to make CI/CD happy
		var wg sync.WaitGroup

		for i := 1; i < totalClients+1; i++ {
			wg.Add(1)

			go func(index int) {
				defer wg.Done()
				mbClient := NewMetalBond(Config{}, client)
				localIP := net.ParseIP("127.0.0.3")
				localIP = incrementIPv4(localIP, index)
				err := mbClient.AddPeer(serverAddress, localIP.String())
				Expect(err).NotTo(HaveOccurred())

				// wait for the peer loop to start
				time.Sleep(1 * time.Second)
				clientAddr := getLocalAddr(mbClient, "")
				Expect(clientAddr).NotTo(Equal(""))

				Expect(waitForPeerState(mbServer, clientAddr, ESTABLISHED)).NotTo(BeFalse())

				p := mbServer.getPeer(clientAddr)

				Expect(waitForPeerState(mbClient, serverAddress, ESTABLISHED)).NotTo(BeFalse())
				vni := VNI(index % 10)
				err = mbClient.Subscribe(vni)
				if err != nil {
					log.Errorf("subscribe failed: %v", err)
				}
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

				// wait for the route to be received
				time.Sleep(3 * time.Second)

				// check if the route was received
				_, exists := p.receivedRoutes.routes[vni][dest][nextHop][p]
				Expect(exists).To(BeTrue())
				Expect(err).NotTo(HaveOccurred())

				// Close the peer
				err = p.metalbond.RemovePeer(p.remoteAddr)
				Expect(err).NotTo(HaveOccurred())

				// expect the peer state to be closed
				Expect(p.GetState()).To(Equal(CLOSED))

				// wait for the peer to be established again
				wait := rand.Intn(20) + 1
				time.Sleep(time.Duration(wait) * time.Second)

				notExcept := clientAddr
				clientAddr = getLocalAddr(mbClient, notExcept)
				if clientAddr == "" {
					log.Errorf("clientAddr is empty '%s'", clientAddr)
				}
				Expect(clientAddr).ShouldNot(BeEmpty())

				// check if the peer is established again
				Expect(waitForPeerState(mbServer, clientAddr, ESTABLISHED)).NotTo(BeFalse())

				p = mbServer.getPeer(clientAddr)

				// wait for the route to be received
				time.Sleep(3 * time.Second)

				// check if the route was received
				_, exists = p.receivedRoutes.routes[vni][dest][nextHop][p]
				if !exists {
					log.Errorf("route not received vni %v, dest %v, nextHop %v, clientAddr %s", vni, dest, nextHop, clientAddr)
					for vni, dest := range p.receivedRoutes.routes {
						log.Errorf("vni %v", vni)
						for dest, nextHop := range dest {
							log.Errorf("dest %v", dest)
							for nextHop, peers := range nextHop {
								log.Errorf("nextHop %v", nextHop)
								for peer := range peers {
									log.Errorf("peer %v", peer)
								}
							}
						}
					}

				}
				Expect(exists).To(BeTrue())
			}(i)
		}
		wg.Wait()
	})

	It("should announce with 2 servers and recover from removing 1 peer", Label("announceHAremove"), func() {
		totalClients := 600 // TODO: was 1000 (local test works for this large value), but it is reduced to this value to make CI/CD happy
		var wg sync.WaitGroup

		for i := 1; i < totalClients+1; i++ {
			wg.Add(1)

			go func(index int) {
				defer GinkgoRecover()
				defer wg.Done()
				mbClient := NewMetalBond(Config{}, client)
				localIP := net.ParseIP("127.0.0.3")
				localIP = incrementIPv4(localIP, index)
				err := mbClient.AddPeer(serverAddress, localIP.String())
				Expect(err).NotTo(HaveOccurred())
				err = mbClient.AddPeer(serverAddress2, localIP.String())
				Expect(err).NotTo(HaveOccurred())

				// mbClient should have both servers as peers
				Expect(len(mbClient.peers)).To(Equal(2))

				// wait for the peer loop to start
				time.Sleep(1 * time.Second)
				clientAddr := getLocalAddr(mbClient, "")
				Expect(clientAddr).NotTo(Equal(""))
				clientAddrS2 := getLocalAddr(mbClient, clientAddr)
				Expect(clientAddr).NotTo(Equal(""))

				// wait for all peerings to be ESTABLISHED
				Expect(waitForPeerState(mbServer, clientAddr, ESTABLISHED)).NotTo(BeFalse())
				Expect(waitForPeerState(mbServer2, clientAddrS2, ESTABLISHED)).NotTo(BeFalse())
				Expect(waitForPeerState(mbClient, serverAddress, ESTABLISHED)).NotTo(BeFalse())

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

				// announce the route
				err = mbClient.AnnounceRoute(vni, dest, nextHop)
				Expect(err).NotTo(HaveOccurred())

				// wait for the route to be received
				time.Sleep(3 * time.Second)

				// check if the route was received by Server1
				p := mbServer.getPeer(clientAddr)
				Expect(p).ToNot(BeNil())
				_, exists := p.receivedRoutes.routes[vni][dest][nextHop][p]
				Expect(exists).To(BeTrue())

				// check if the route was received by Server2
				p = mbServer2.getPeer(clientAddrS2)
				Expect(p).ToNot(BeNil())
				_, exists = p.receivedRoutes.routes[vni][dest][nextHop][p]
				Expect(exists).To(BeTrue())

				Expect(len(mbServer.routeTable.routes[vni][dest][nextHop])).To(Equal(1))
				Expect(len(mbServer2.routeTable.routes[vni][dest][nextHop])).To(Equal(1))
				Expect(len(mbClient.routeTable.routes)).To(Equal(1))

				By("Removing the Servers from peers")
				// remove Server1 from peers
				err = mbClient.RemovePeer(serverAddress)
				Expect(err).NotTo(HaveOccurred())
				time.Sleep(2 * time.Second)

				p = mbServer.getPeer(clientAddr)
				Expect(p).To(BeNil())
				p = mbServer2.getPeer(clientAddrS2)
				Expect(p.GetState()).To(Equal(ESTABLISHED))

				// check if the route was received by Server2
				_, exists = p.receivedRoutes.routes[vni][dest][nextHop][p]
				Expect(exists).To(BeTrue())

				Expect(len(mbServer.routeTable.routes[vni][dest][nextHop])).To(Equal(0))
				Expect(len(mbServer2.routeTable.routes[vni][dest][nextHop])).To(Equal(1))
				Expect(len(mbClient.routeTable.routes)).To(Equal(1))

				// restore peering with Server1
				err = mbClient.AddPeer(serverAddress, localIP.String())
				Expect(err).NotTo(HaveOccurred())
				clientAddr = getLocalAddr(mbClient, clientAddrS2)
				Expect(waitForPeerState(mbServer, clientAddr, ESTABLISHED)).NotTo(BeFalse())

				Expect(len(mbServer.routeTable.routes[vni][dest][nextHop])).To(Equal(1))
				Expect(len(mbServer2.routeTable.routes[vni][dest][nextHop])).To(Equal(1))
				Expect(len(mbClient.routeTable.routes)).To(Equal(1))

				// check if the route was received by Server1
				p = mbServer.getPeer(clientAddr)
				Expect(p.GetState()).To(Equal(ESTABLISHED))
				_, exists = p.receivedRoutes.routes[vni][dest][nextHop][p]
				Expect(exists).To(BeTrue())

				// remove Server2 from peers
				err = mbClient.RemovePeer(serverAddress2)
				Expect(err).NotTo(HaveOccurred())
				time.Sleep(2 * time.Second)

				p = mbServer2.getPeer(clientAddrS2)
				Expect(p).To(BeNil())

				Expect(len(mbServer.routeTable.routes[vni][dest][nextHop])).To(Equal(1))
				Expect(len(mbServer2.routeTable.routes[vni][dest][nextHop])).To(Equal(0))
				Expect(len(mbClient.routeTable.routes)).To(Equal(1))

				// restore peer with Server2
				err = mbClient.AddPeer(serverAddress2, localIP.String())
				Expect(err).NotTo(HaveOccurred())
				clientAddrS2 = getLocalAddr(mbClient, clientAddr)
				Expect(waitForPeerState(mbServer2, clientAddrS2, ESTABLISHED)).NotTo(BeFalse())

				Expect(len(mbServer.routeTable.routes[vni][dest][nextHop])).To(Equal(1))
				Expect(len(mbServer2.routeTable.routes[vni][dest][nextHop])).To(Equal(1))
				Expect(len(mbClient.routeTable.routes)).To(Equal(1))

				// check if the route was received by Server2
				p = mbServer2.getPeer(clientAddrS2)
				Expect(p.GetState()).To(Equal(ESTABLISHED))
				_, exists = p.receivedRoutes.routes[vni][dest][nextHop][p]
				Expect(exists).To(BeTrue())
			}(i)
		}
		wg.Wait()
	})

	It("should announce with 2 servers and recover from shutdown of 1 server", Label("announceHAshutdown"), func() {
		mbClient := NewMetalBond(Config{}, client)
		localIP := net.ParseIP("127.0.0.3")
		err := mbClient.AddPeer(serverAddress, localIP.String())
		Expect(err).NotTo(HaveOccurred())
		err = mbClient.AddPeer(serverAddress2, localIP.String())
		Expect(err).NotTo(HaveOccurred())

		// mbClient should have both servers as peers
		Expect(len(mbClient.peers)).To(Equal(2))

		// wait for the peer loop to start
		time.Sleep(1 * time.Second)
		clientAddr := getLocalAddr(mbClient, "")
		Expect(clientAddr).NotTo(Equal(""))
		clientAddrS2 := getLocalAddr(mbClient, clientAddr)
		Expect(clientAddr).NotTo(Equal(""))

		// wait for all peerings to be ESTABLISHED
		Expect(waitForPeerState(mbServer, clientAddr, ESTABLISHED)).NotTo(BeFalse())
		Expect(waitForPeerState(mbServer2, clientAddrS2, ESTABLISHED)).NotTo(BeFalse())
		Expect(waitForPeerState(mbClient, serverAddress, ESTABLISHED)).NotTo(BeFalse())

		vni := VNI(10)
		err = mbClient.Subscribe(vni)
		Expect(err).NotTo(HaveOccurred())

		// prepare the route
		startIP := net.ParseIP("100.64.0.0")
		ip := incrementIPv4(startIP, 1)
		addr, err := netip.ParseAddr(ip.String())
		Expect(err).NotTo(HaveOccurred())
		underlayRoute, err := netip.ParseAddr(fmt.Sprintf("b198:5b10:3880:fd32:fb80:80dd:46f7:%d", 1))
		Expect(err).NotTo(HaveOccurred())
		dest := Destination{
			Prefix:    netip.PrefixFrom(addr, 32),
			IPVersion: IPV4,
		}
		nextHop := NextHop{
			TargetVNI:     uint32(vni),
			TargetAddress: underlayRoute,
		}

		// announce the route
		err = mbClient.AnnounceRoute(vni, dest, nextHop)
		Expect(err).NotTo(HaveOccurred())

		// wait for the route to be received
		time.Sleep(3 * time.Second)

		// check if the route was received by Server1
		p := mbServer.getPeer(clientAddr)
		Expect(p).ToNot(BeNil())
		_, exists := p.receivedRoutes.routes[vni][dest][nextHop][p]
		Expect(exists).To(BeTrue())

		// check if the route was received by Server2
		p = mbServer2.getPeer(clientAddrS2)
		Expect(p).ToNot(BeNil())
		_, exists = p.receivedRoutes.routes[vni][dest][nextHop][p]
		Expect(exists).To(BeTrue())

		Expect(len(mbServer.routeTable.routes[vni][dest][nextHop])).To(Equal(1))
		Expect(len(mbServer2.routeTable.routes[vni][dest][nextHop])).To(Equal(1))
		Expect(len(mbClient.routeTable.routes)).To(Equal(1))

		By("Shuting down the Servers")
		// shut down Server1
		mbServer.Shutdown()
		// wait for keepalive to run out
		time.Sleep(time.Duration(mbClient.keepaliveInterval)*time.Second + 1*time.Second)

		p = mbServer.getPeer(clientAddr)
		Expect(p).To(BeNil())
		p = mbServer2.getPeer(clientAddrS2)
		Expect(p.GetState()).To(Equal(ESTABLISHED))

		// check if the route was received by Server2
		_, exists = p.receivedRoutes.routes[vni][dest][nextHop][p]

		Expect(exists).To(BeTrue())
		Expect(len(mbServer.routeTable.routes[vni][dest][nextHop])).To(Equal(0))
		Expect(len(mbServer2.routeTable.routes[vni][dest][nextHop])).To(Equal(1))
		Expect(len(mbClient.routeTable.routes)).To(Equal(1))

		// start Server1 again
		// to reuse same mbServer struct, reset shuttingDown to false
		mbServer.shuttingDown = false
		err = mbServer.StartServer(serverAddress)
		Expect(err).ToNot(HaveOccurred())
		time.Sleep(2 * time.Second)

		clientAddr = getLocalAddr(mbClient, clientAddrS2)
		Expect(waitForPeerState(mbServer, clientAddr, ESTABLISHED)).NotTo(BeFalse())

		Expect(len(mbServer.routeTable.routes[vni][dest][nextHop])).To(Equal(1))
		Expect(len(mbServer2.routeTable.routes[vni][dest][nextHop])).To(Equal(1))
		Expect(len(mbClient.routeTable.routes)).To(Equal(1))

		// check if the route was received by Server1
		p = mbServer.getPeer(clientAddr)
		Expect(p.GetState()).To(Equal(ESTABLISHED))
		_, exists = p.receivedRoutes.routes[vni][dest][nextHop][p]
		Expect(exists).To(BeTrue())

		// shut down Server2
		mbServer2.Shutdown()
		// wait for keepalive to run out
		time.Sleep(time.Duration(mbClient.keepaliveInterval)*time.Second + 1*time.Second)

		p = mbServer2.getPeer(clientAddrS2)
		Expect(p).To(BeNil())

		Expect(len(mbServer.routeTable.routes[vni][dest][nextHop])).To(Equal(1))
		Expect(len(mbServer2.routeTable.routes[vni][dest][nextHop])).To(Equal(0))
		Expect(len(mbClient.routeTable.routes)).To(Equal(1))

		// start Server2 again
		// to reuse same mbServer2 struct, reset shuttingDown to false
		mbServer2.shuttingDown = false
		err = mbServer2.StartServer(serverAddress2)
		Expect(err).ToNot(HaveOccurred())
		time.Sleep(2 * time.Second)

		clientAddrS2 = getLocalAddr(mbClient, clientAddr)
		Expect(waitForPeerState(mbServer2, clientAddrS2, ESTABLISHED)).NotTo(BeFalse())

		Expect(len(mbServer.routeTable.routes[vni][dest][nextHop])).To(Equal(1))
		Expect(len(mbServer2.routeTable.routes[vni][dest][nextHop])).To(Equal(1))
		Expect(len(mbClient.routeTable.routes)).To(Equal(1))

		// check if the route was received by Server2
		p = mbServer2.getPeer(clientAddrS2)
		Expect(p.GetState()).To(Equal(ESTABLISHED))
		_, exists = p.receivedRoutes.routes[vni][dest][nextHop][p]
		Expect(exists).To(BeTrue())

	})
})

func waitForPeerState(mbServer *MetalBond, clientAddr string, expectedState ConnectionState) bool {

	// Call the checkPeerState function repeatedly until it returns true or a timeout is reached
	timeout := 30 * time.Second
	start := time.Now()
	for {
		peer := mbServer.getPeer(clientAddr)

		if peer != nil && peer.GetState() == expectedState {
			return true
		}

		if time.Since(start) >= timeout {
			state := "NONE"
			if peer != nil {
				state = peer.GetState().String()
			}
			log.Errorf("Timeout reached while waiting for peer (%s) to reach expected state %s, but state is %s", clientAddr, expectedState, state)
			return false
		}

		// Wait a short time before checking again
		time.Sleep(500 * time.Millisecond)
	}
}

func getLocalAddr(mbClient *MetalBond, notExcept string) string {
	timeout := 30 * time.Second
	start := time.Now()
	for {
		for _, peer := range mbClient.peers {
			if peer.localAddr != "" && peer.localAddr != notExcept {
				return peer.localAddr
			}
		}

		if time.Since(start) >= timeout {
			return ""
		}

		// Wait a short time before checking again
		time.Sleep(500 * time.Millisecond)
	}
}

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

func (m *MetalBond) getPeer(peerAddress string) *metalBondPeer {
	m.mtxPeers.RLock()
	peer := m.peers[peerAddress]
	m.mtxPeers.RUnlock()

	if peer != nil {
		return peer
	} else {
		return nil
	}
}
