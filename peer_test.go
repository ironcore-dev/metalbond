// Copyright 2023 IronCore authors
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
		localIP := net.ParseIP("127.0.0.2")
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

	It("should reset", func() {
		mbClient := NewMetalBond(Config{}, client)
		err := mbClient.AddPeer(serverAddress, "127.0.0.2")
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

	It("should reconnect", func() {
		mbClient := NewMetalBond(Config{}, client)
		err := mbClient.AddPeer(serverAddress, "127.0.0.2")
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

	It("client timeout", func() {
		mbClient := NewMetalBond(Config{}, client)
		err := mbClient.AddPeer(serverAddress, "127.0.0.2")
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

	It("should announce", func() {
		totalClients := 600 // TODO: was 1000 (local test works for this large value), but it is reduced to this value to make CI/CD happy
		var wg sync.WaitGroup

		for i := 1; i < totalClients+1; i++ {
			wg.Add(1)

			go func(index int) {
				defer wg.Done()
				mbClient := NewMetalBond(Config{}, client)
				localIP := net.ParseIP("127.0.0.2")
				localIP = incrementIPv4(localIP, index)
				err := mbClient.AddPeer(serverAddress, localIP.String())
				Expect(err).NotTo(HaveOccurred())

				// wait for the peer loop to start
				time.Sleep(1 * time.Second)
				clientAddr := getLocalAddr(mbClient, "")
				Expect(clientAddr).NotTo(Equal(""))

				Expect(waitForPeerState(mbServer, clientAddr, ESTABLISHED)).NotTo(BeFalse())

				mbServer.mtxPeers.RLock()
				p := mbServer.peers[clientAddr]
				mbServer.mtxPeers.RUnlock()

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

				mbServer.mtxPeers.RLock()
				p = mbServer.peers[clientAddr]
				mbServer.mtxPeers.RUnlock()

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
})

func waitForPeerState(mbServer *MetalBond, clientAddr string, expectedState ConnectionState) bool {

	// Call the checkPeerState function repeatedly until it returns true or a timeout is reached
	timeout := 30 * time.Second
	start := time.Now()
	for {
		mbServer.mtxPeers.RLock()
		peer := mbServer.peers[clientAddr]
		mbServer.mtxPeers.RUnlock()

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
