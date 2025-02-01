package metalbond

import (
	"bytes"
	"fmt"
	"github.com/ironcore-dev/metalbond/pb"
	"google.golang.org/protobuf/proto"
	"math/rand"
	"net"
	"net/netip"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	log "github.com/sirupsen/logrus"
)

const (
	serverTxChanCapacity           = 2048
	serverRxChanEventCapacity      = 10
	serverRxChanDataUpdateCapacity = 100

	clientTxChanCapacity           = 100
	clientRxChanEventCapacity      = 10
	clientRxChanDataUpdateCapacity = 50
)

var _ = Describe("RxLoop", func() {
	var (
		server net.Conn
		client net.Conn
		peer   *metalBondPeer
	)

	BeforeEach(func() {
		server, client = net.Pipe() // Simulate TCP connection
		peer = newTestMetalBondPeer(client)
		peer.keepaliveInterval = 5
		go peer.rxLoop()
	})

	AfterEach(func() {
		_ = server.Close()
		_ = client.Close()
	})

	Context("Packet Processing", func() {
		It("should correctly receive a complete HELLO packet", func() {
			pkt := createPacket(1, HELLO, mockHelloMessage()) // HELLO packet
			_, err := server.Write(pkt)
			Expect(err).NotTo(HaveOccurred())

			Eventually(peer.rxHello).Should(Receive(), "Expected HELLO packet to be received")
		})

		It("should correctly handle fragmented packet reception", func() {
			pkt := createPacket(1, SUBSCRIBE, mockSubscribeMessage()) // SUBSCRIBE packet
			_, err := server.Write(pkt[:2])                           // Send header first
			Expect(err).NotTo(HaveOccurred())
			time.Sleep(500 * time.Millisecond)
			_, err = server.Write(pkt[2:]) // Send the rest after a delay
			Expect(err).NotTo(HaveOccurred())

			Eventually(peer.rxSubscribe).Should(Receive(), "Expected fragmented SUBSCRIBE packet to be reassembled")
		})

		It("should reset to CLOSED if oversized packet is received after handshake", func() {
			peer.setState(ESTABLISHED) // Simulate successful handshake

			pkt := createPacket(1, UPDATE, mockOversizedMessage()) // Oversized UPDATE packet
			_, err := server.Write(pkt)
			Expect(err).NotTo(HaveOccurred())

			time.Sleep(5 * time.Second)
			Eventually(func() ConnectionState {
				return peer.GetState()
			}).Should(Equal(CLOSED), "Expected connection to transition to CLOSED after oversized packet")
		})

		It("should reset to CLOSED on invalid protocol version", func() {
			pkt := createPacket(2, HELLO, mockHelloMessage()) // Invalid version
			_, err := server.Write(pkt)
			Expect(err).NotTo(HaveOccurred())

			time.Sleep(5 * time.Second)
			Eventually(func() ConnectionState {
				return peer.GetState()
			}).Should(Equal(CLOSED), "Expected connection to transition to CLOSED after invalid version")
		})

		It("should reset on read timeout and transition to CLOSED", func() {
			pkt := createPacket(1, HELLO, mockHelloMessage()) // Invalid version
			_, err := server.Write(pkt)
			Expect(err).NotTo(HaveOccurred())
			peer.setState(ESTABLISHED) // Simulate successful handshake

			time.Sleep(61 * time.Second) // Exceed 61-second timeout

			Eventually(func() ConnectionState {
				return peer.GetState()
			}).Should(Equal(CLOSED), "Expected connection to reset on read timeout and transition to CLOSED")
		})

		It("should correctly process multiple packets in one read", func() {
			pkt1 := createPacket(1, HELLO, mockHelloMessage())
			pkt2 := createPacket(1, SUBSCRIBE, mockSubscribeMessage())
			_, err := server.Write(append(pkt1, pkt2...)) // Send HELLO and SUBSCRIBE together
			Expect(err).NotTo(HaveOccurred())

			Eventually(peer.rxHello).Should(Receive(), "Expected to receive HELLO packet")
			Eventually(peer.rxSubscribe).Should(Receive(), "Expected to receive SUBSCRIBE packet")
		})
	})

	Context("Keepalive Handling", func() {
		It("should handle delayed KEEPALIVE response", func() {
			pkt := createPacket(1, KEEPALIVE, mockKeepaliveMessage()) // KEEPALIVE packet
			time.Sleep(20 * time.Second)                              // Simulate keepalive delay
			_, err := server.Write(pkt)
			Expect(err).NotTo(HaveOccurred())

			Eventually(peer.rxKeepalive).Should(Receive(), "Expected delayed KEEPALIVE to be processed")
		})
	})
})

var _ = Describe("Peer", func() {

	var (
		mbServer1      *MetalBond
		mbServer2      *MetalBond
		serverAddress1 string
		serverAddress2 string
		dummyClient    *DummyClient
	)

	BeforeEach(func() {
		log.Info("----- START -----")
		config := Config{
			KeepaliveInterval: 5,
		}
		dummyClient = NewDummyClient()

		mbServer1 = NewMetalBond(config, dummyClient)
		serverAddress1 = fmt.Sprintf("127.0.0.1:%d", getRandomTCPPort())
		err := mbServer1.StartServer(serverAddress1, serverTxChanCapacity, serverRxChanEventCapacity, serverRxChanDataUpdateCapacity)
		Expect(err).ToNot(HaveOccurred())

		mbServer2 = NewMetalBond(config, dummyClient)
		serverAddress2 = fmt.Sprintf("127.0.0.1:%d", getRandomTCPPort())
		err = mbServer2.StartServer(serverAddress2, serverTxChanCapacity, serverRxChanEventCapacity, serverRxChanDataUpdateCapacity)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		mbServer1.Shutdown()
		mbServer2.Shutdown()
	})

	It("should keepalive", func() {
		mbClient := NewMetalBond(Config{
			KeepaliveInterval: 5,
		}, dummyClient)
		localIP := net.ParseIP("127.0.0.2")
		err := mbClient.AddPeer(serverAddress1, localIP.String(), clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())

		time.Sleep(60 * time.Second)

		clientAddr := getLocalAddr(mbClient, "")
		Expect(clientAddr).NotTo(Equal(""))

		Expect(waitForPeerState(mbServer1, clientAddr, ESTABLISHED)).NotTo(BeFalse())
		state, err := mbServer1.PeerState(clientAddr)
		Expect(err).NotTo(HaveOccurred())
		Expect(state).To(Equal(ESTABLISHED))

		mbClient.Shutdown()
	})

	It("should subscribe", func() {
		mbClient := NewMetalBond(Config{
			KeepaliveInterval: 5,
		}, dummyClient)
		localIP := net.ParseIP("127.0.0.2")
		err := mbClient.AddPeer(serverAddress1, localIP.String(), clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())

		time.Sleep(5 * time.Second)
		vni := VNI(200)
		err = mbClient.Subscribe(vni)
		Expect(err).NotTo(HaveOccurred())

		vnis := mbClient.GetSubscribedVnis()
		Expect(len(vnis)).To(Equal(1))
		Expect(vnis[0]).To(Equal(vni))

		err = mbClient.Unsubscribe(vni)
		Expect(err).NotTo(HaveOccurred())

		vnis = mbClient.GetSubscribedVnis()
		Expect(len(vnis)).To(Equal(0))

		err = mbClient.RemovePeer(serverAddress1)
		Expect(err).NotTo(HaveOccurred())

		mbClient.Shutdown()
	})

	It("should reset", func() {
		mbClient := NewMetalBond(Config{
			KeepaliveInterval: 5,
		}, dummyClient)
		err := mbClient.AddPeer(serverAddress1, "127.0.0.2", clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())

		clientAddr := getLocalAddr(mbClient, "")
		Expect(clientAddr).NotTo(Equal(""))

		Expect(waitForPeerState(mbServer1, clientAddr, ESTABLISHED)).NotTo(BeFalse())
		state, err := mbServer1.PeerState(clientAddr)
		Expect(err).NotTo(HaveOccurred())
		Expect(state).To(Equal(ESTABLISHED))

		var p *metalBondPeer
		for _, peer := range mbServer1.peers {
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
		Expect(waitForPeerState(mbServer1, clientAddr, ESTABLISHED)).NotTo(BeFalse())
	})

	It("should reconnect", func() {
		mbClient := NewMetalBond(Config{
			KeepaliveInterval: 5,
		}, dummyClient)
		err := mbClient.AddPeer(serverAddress1, "127.0.0.2", clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())

		clientAddr := getLocalAddr(mbClient, "")
		Expect(clientAddr).NotTo(Equal(""))

		Expect(waitForPeerState(mbServer1, clientAddr, ESTABLISHED)).NotTo(BeFalse())

		var p *metalBondPeer
		for _, peer := range mbServer1.peers {
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
		Expect(waitForPeerState(mbServer1, clientAddr, ESTABLISHED)).NotTo(BeFalse())
	})

	It("metalbond timeout", func() {
		mbClient := NewMetalBond(Config{
			KeepaliveInterval: 5,
		}, dummyClient)
		err := mbClient.AddPeer(serverAddress1, "127.0.0.2", clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())

		clientAddr := getLocalAddr(mbClient, "")
		Expect(clientAddr).NotTo(Equal(""))

		Expect(waitForPeerState(mbServer1, clientAddr, ESTABLISHED)).NotTo(BeFalse())

		vni := VNI(200)
		err = mbClient.Subscribe(vni)
		Expect(err).NotTo(HaveOccurred())

		var serverPeer *metalBondPeer
		for _, peer := range mbServer1.peers {
			serverPeer = peer
			break
		}

		var clientPeer *metalBondPeer
		for _, peer := range mbClient.peers {
			clientPeer = peer
			break
		}

		err = mbClient.Unsubscribe(vni)
		Expect(err).NotTo(HaveOccurred())

		serverPeer.stopReceive = true

		time.Sleep(12 * time.Second)

		// expect the peer state to be closed
		Expect(clientPeer.GetState()).To(Equal(RETRY))

		err = mbClient.RemovePeer(serverAddress1)
		Expect(err).NotTo(HaveOccurred())
	})

	It("dummyClient timeout", func() {
		mbClient := NewMetalBond(Config{
			KeepaliveInterval: 5,
		}, dummyClient)
		err := mbClient.AddPeer(serverAddress1, "127.0.0.2", clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())

		clientAddr := getLocalAddr(mbClient, "")
		Expect(clientAddr).NotTo(Equal(""))

		Expect(waitForPeerState(mbServer1, clientAddr, ESTABLISHED)).NotTo(BeFalse())

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

		err = mbClient.RemovePeer(serverAddress1)
		Expect(err).NotTo(HaveOccurred())
	})

	It("should cleanup announcements on unsubscribe", func() {
		mbClient := NewMetalBond(Config{
			KeepaliveInterval: 5,
		}, dummyClient)
		err := mbClient.AddPeer(serverAddress1, "127.0.0.2", clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())

		clientAddr := getLocalAddr(mbClient, "")
		Expect(clientAddr).NotTo(Equal(""))

		Expect(waitForPeerState(mbServer1, clientAddr, ESTABLISHED)).NotTo(BeFalse())

		vni := VNI(200)
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

		err = mbClient.AnnounceRoute(vni, dest, nextHop)
		Expect(err).NotTo(HaveOccurred())

		Expect(mbClient.IsRouteAnnounced(vni, dest, nextHop)).To(BeTrue())

		// wait for the route to be received
		time.Sleep(3 * time.Second)

		myAnnouncements := mbClient.GetAnnouncementsForVni(vni)
		Expect(len(myAnnouncements)).To(Equal(1))

		err = mbClient.Unsubscribe(vni)
		Expect(err).NotTo(HaveOccurred())

		myAnnouncements = mbClient.GetAnnouncementsForVni(vni)
		Expect(len(myAnnouncements)).To(Equal(0))
	})

	It("should distribute routes if one peer is closed", func() {
		mbClient1 := NewMetalBond(Config{
			KeepaliveInterval: 5,
		}, dummyClient)
		localIP1 := net.ParseIP("127.0.0.2")
		err := mbClient1.AddPeer(serverAddress1, localIP1.String(), clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())
		err = mbClient1.AddPeer(serverAddress2, localIP1.String(), clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())

		mbClient2 := NewMetalBond(Config{
			KeepaliveInterval: 5,
		}, dummyClient)
		localIP2 := net.ParseIP("127.0.0.3")
		err = mbClient2.AddPeer(serverAddress1, localIP2.String(), clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())
		err = mbClient2.AddPeer(serverAddress2, localIP2.String(), clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())

		time.Sleep(5 * time.Second)
		vni := VNI(200)
		err = mbClient1.Subscribe(vni)
		Expect(err).NotTo(HaveOccurred())

		err = mbClient2.Subscribe(vni)
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

		clientAddr := getLocalAddr(mbClient1, "")
		Expect(clientAddr).NotTo(Equal(""))

		mbClient1.peers[serverAddress1].state = CLOSED

		err = mbClient1.AnnounceRoute(vni, dest, nextHop)
		Expect(err).To(HaveOccurred())

		// wait for the route to be received
		time.Sleep(3 * time.Second)

		mbClient1Routes := len(mbClient1.routeTable.routes[vni][dest.String()][nextHop])
		Expect(mbClient1Routes).To(Equal(0))

		mbClient2Routes := len(mbClient2.routeTable.routes[vni][dest.String()][nextHop])
		Expect(mbClient2Routes).To(Equal(1))
	})

	It("should remove properly", func() {
		mbClient := NewMetalBond(Config{
			KeepaliveInterval: 5,
		}, dummyClient)
		err := mbClient.AddPeer(serverAddress1, "127.0.0.2", clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())

		clientAddr := getLocalAddr(mbClient, "")
		Expect(clientAddr).NotTo(Equal(""))

		Expect(waitForPeerState(mbServer1, clientAddr, ESTABLISHED)).NotTo(BeFalse())

		vni := VNI(200)
		err = mbClient.Subscribe(vni)
		Expect(err).NotTo(HaveOccurred())

		Expect(mbClient.IsSubscribed(vni)).To(BeTrue())

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

		err = mbClient.AnnounceRoute(vni, dest, nextHop)
		Expect(err).NotTo(HaveOccurred())

		// wait for the route to be received
		time.Sleep(3 * time.Second)

		err = mbClient.GetRoutesForVni(vni)
		Expect(err).NotTo(HaveOccurred())

		// Close the keepalive
		mbServer1.Shutdown()
		mbServer1.peers[clientAddr].Close()
		time.Sleep(10 * time.Second)

		err = mbClient.RemovePeer(serverAddress1)
		Expect(err).NotTo(HaveOccurred())
	})

	It("should get routes for vni", func() {
		mbClient := NewMetalBond(Config{
			KeepaliveInterval: 5,
		}, dummyClient)
		err := mbClient.AddPeer(serverAddress1, "127.0.0.2", clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())

		clientAddr := getLocalAddr(mbClient, "")
		Expect(clientAddr).NotTo(Equal(""))

		Expect(waitForPeerState(mbServer1, clientAddr, ESTABLISHED)).NotTo(BeFalse())

		vni := VNI(200)
		err = mbClient.Subscribe(vni)
		Expect(err).NotTo(HaveOccurred())

		Expect(mbClient.IsSubscribed(vni)).To(BeTrue())

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

		err = mbClient.AnnounceRoute(vni, dest, nextHop)
		Expect(err).NotTo(HaveOccurred())

		// wait for the route to be received
		time.Sleep(3 * time.Second)

		err = mbClient.GetRoutesForVni(vni)
		Expect(err).NotTo(HaveOccurred())
	})

	It("multiple metalbond reconnect", func() {
		mbClient1 := NewMetalBond(Config{
			KeepaliveInterval: 5,
		}, dummyClient)
		localIP1 := net.ParseIP("127.0.0.2")
		err := mbClient1.AddPeer(serverAddress1, localIP1.String(), clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())
		err = mbClient1.AddPeer(serverAddress2, localIP1.String(), clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())

		mbClient2 := NewMetalBond(Config{
			KeepaliveInterval: 5,
		}, dummyClient)
		localIP2 := net.ParseIP("127.0.0.3")
		err = mbClient2.AddPeer(serverAddress1, localIP2.String(), clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())
		err = mbClient2.AddPeer(serverAddress2, localIP2.String(), clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())

		time.Sleep(5 * time.Second)
		vni := VNI(200)
		err = mbClient1.Subscribe(vni)
		Expect(err).NotTo(HaveOccurred())

		err = mbClient2.Subscribe(vni)
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

		err = mbClient1.AnnounceRoute(vni, dest, nextHop)
		Expect(err).NotTo(HaveOccurred())

		// wait for the route to be received
		time.Sleep(3 * time.Second)

		mbClient1Routes := len(mbClient1.routeTable.routes[vni][dest.String()][nextHop])
		Expect(mbClient1Routes).To(Equal(0))

		mbClient2Routes := len(mbClient2.routeTable.routes[vni][dest.String()][nextHop])
		Expect(mbClient2Routes).To(Equal(2))

		for _, peer := range mbServer1.peers {
			peer.Reset()
		}

		time.Sleep(1 * time.Second)

		mbClient1Routes = len(mbClient1.routeTable.routes[vni][dest.String()][nextHop])
		Expect(mbClient1Routes).To(Equal(0))

		mbClient2Routes = len(mbClient2.routeTable.routes[vni][dest.String()][nextHop])
		Expect(mbClient2Routes).To(Equal(1))

		time.Sleep(10 * time.Second)

		mbClient1Routes = len(mbClient1.routeTable.routes[vni][dest.String()][nextHop])
		Expect(mbClient1Routes).To(Equal(0))

		mbClient2Routes = len(mbClient2.routeTable.routes[vni][dest.String()][nextHop])
		Expect(mbClient2Routes).To(Equal(2))

		err = mbClient1.WithdrawRoute(vni, dest, nextHop)
		Expect(err).NotTo(HaveOccurred())

		time.Sleep(2 * time.Second)

		mbClient1Routes = len(mbClient1.routeTable.routes[vni][dest.String()][nextHop])
		Expect(mbClient1Routes).To(Equal(0))

		mbClient2Routes = len(mbClient2.routeTable.routes[vni][dest.String()][nextHop])
		Expect(mbClient2Routes).To(Equal(0))
	})

	It("multiple metalbond announce and subscribe", func() {
		mbClient1 := NewMetalBond(Config{
			KeepaliveInterval: 5,
		}, dummyClient)
		localIP1 := net.ParseIP("127.0.0.2")
		err := mbClient1.AddPeer(serverAddress1, localIP1.String(), clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())
		err = mbClient1.AddPeer(serverAddress2, localIP1.String(), clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
		Expect(err).NotTo(HaveOccurred())

		time.Sleep(5 * time.Second)
		vni := VNI(200)

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

		err = mbClient1.AnnounceRoute(vni, dest, nextHop)
		Expect(err).NotTo(HaveOccurred())

		// wait for the route to be received
		time.Sleep(3 * time.Second)

		err = mbClient1.Subscribe(vni)
		Expect(err).NotTo(HaveOccurred())

		time.Sleep(2 * time.Second)

		mbClient1Routes := len(mbClient1.routeTable.routes[vni][dest.String()][nextHop])
		Expect(mbClient1Routes).To(Equal(0))
	})

	It("should announce", func() {
		totalClients := 600
		var wg sync.WaitGroup

		for i := 1; i <= totalClients; i++ {
			wg.Add(1)

			go func(index int) {
				defer wg.Done()
				mbClient := NewMetalBond(Config{
					KeepaliveInterval: 5,
				}, dummyClient)
				localIP := net.ParseIP("127.0.0.2")
				localIP = incrementIPv4(localIP, index)
				err := mbClient.AddPeer(serverAddress1, localIP.String(), clientTxChanCapacity, clientRxChanEventCapacity, clientRxChanDataUpdateCapacity)
				Expect(err).NotTo(HaveOccurred())

				// wait for the peer loop to start
				time.Sleep(1 * time.Second)
				clientAddr := getLocalAddr(mbClient, "")
				Expect(clientAddr).NotTo(Equal(""))

				Expect(waitForPeerState(mbServer1, clientAddr, ESTABLISHED)).NotTo(BeFalse())

				mbServer1.mtxPeers.RLock()
				p := mbServer1.peers[clientAddr]
				mbServer1.mtxPeers.RUnlock()

				Expect(waitForPeerState(mbClient, serverAddress1, ESTABLISHED)).NotTo(BeFalse())
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

				// wait for the route to be received
				time.Sleep(3 * time.Second)

				// check if the route was received
				_, exists := p.receivedRoutes.routes[vni][dest.String()][nextHop][p]
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
				Expect(clientAddr).NotTo(BeEmpty())

				// check if the peer is established again
				Expect(waitForPeerState(mbServer1, clientAddr, ESTABLISHED)).NotTo(BeFalse())

				mbServer1.mtxPeers.RLock()
				p = mbServer1.peers[clientAddr]
				mbServer1.mtxPeers.RUnlock()

				// wait for the route to be received
				time.Sleep(3 * time.Second)

				// check if the route was received
				_, exists = p.receivedRoutes.routes[vni][dest.String()][nextHop][p]
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

func createPacket(version byte, msgType MESSAGE_TYPE, payload []byte) []byte {
	pktLen := uint16(len(payload))
	header := []byte{version, byte(pktLen >> 8), byte(pktLen & 0xFF), byte(msgType)}
	return append(header, payload...)
}

// Properly serialize HELLO message
func mockHelloMessage() []byte {
	msg := &pb.Hello{
		KeepaliveInterval: 30,
		IsServer:          true,
	}
	serialized, _ := proto.Marshal(msg)
	return serialized
}

// Properly serialize SUBSCRIBE message
func mockSubscribeMessage() []byte {
	msg := &pb.Subscription{
		Vni: 200,
	}
	serialized, _ := proto.Marshal(msg)
	return serialized
}

// Properly serialize KEEPALIVE message (empty payload)
func mockKeepaliveMessage() []byte {
	return []byte{} // KEEPALIVE doesn't need a payload
}

// Properly serialize an oversized message for testing
func mockOversizedMessage() []byte {
	return bytes.Repeat([]byte{0x55}, 1189) // Exceeds the 1188-byte limit
}

func newTestMetalBondPeer(conn net.Conn) *metalBondPeer {

	config := Config{
		KeepaliveInterval: 5,
	}
	dummyClient := NewDummyClient()

	return &metalBondPeer{
		conn:                     &conn,
		txChanCapacity:           10,
		rxChanEventCapacity:      10,
		rxChanDataUpdateCapacity: 10,
		rxHello:                  make(chan msgHello, 1),
		rxKeepalive:              make(chan msgKeepalive, 1),
		rxSubscribe:              make(chan msgSubscribe, 1),
		rxUnsubscribe:            make(chan msgUnsubscribe, 1),
		rxUpdate:                 make(chan msgUpdate, 1),
		shutdown:                 make(chan bool, 1),
		keepaliveStop:            make(chan bool, 1),
		metalbond:                NewMetalBond(config, dummyClient),
	}
}
