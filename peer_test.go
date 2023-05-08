// Copyright 2023 OnMetal authors
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

	It("should reset", func() {
		mbClient := NewMetalBond(Config{}, client)
		err := mbClient.AddPeer(serverAddress)
		Expect(err).NotTo(HaveOccurred())

		Expect(waitForPeerState(mbServer, ESTABLISHED)).NotTo(BeFalse())

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

		// wait for the peer to be established again
		Expect(waitForPeerState(mbServer, ESTABLISHED)).NotTo(BeFalse())
	})

	It("should reconnect", func() {
		mbClient := NewMetalBond(Config{}, client)
		err := mbClient.AddPeer(serverAddress)
		Expect(err).NotTo(HaveOccurred())

		Expect(waitForPeerState(mbServer, ESTABLISHED)).NotTo(BeFalse())

		var p *metalBondPeer
		for _, peer := range mbServer.peers {
			p = peer
			break
		}

		// Close the peer
		p.Close()

		//
		Expect(p.GetState()).To(Equal(CLOSED))

		// wait for the peer to be established again
		Expect(waitForPeerState(mbServer, ESTABLISHED)).NotTo(BeFalse())
	})
})

func waitForPeerState(mbServer *MetalBond, expectedState ConnectionState) bool {
	return Eventually(func() bool {
		for _, peer := range mbServer.peers {
			if peer.GetState() == expectedState {
				return true
			}
		}
		return false
	}, 10*time.Second).Should(BeTrue(), fmt.Sprintf("expected peer state to be %s", expectedState))
}
