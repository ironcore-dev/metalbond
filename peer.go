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
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/sirupsen/logrus"
)

var RETRY_INTERVAL = time.Duration(5 * time.Second)

type metalBondPeer struct {
	redisClient *redis.Client
	remoteAddr  string
	direction   ConnectionDirection

	mtxState sync.RWMutex
	state    ConnectionState

	receivedRoutes routeTable
	subscribedVNIs map[VNI]bool

	metalbond *MetalBond

	keepaliveInterval uint32

	shutdown      chan bool
	keepaliveStop chan bool
	txChan        chan []byte
	wg            sync.WaitGroup
}

func newMetalBondPeer(
	pconn *net.Conn,
	remoteAddr string,
	keepaliveInterval uint32,
	direction ConnectionDirection,
	metalbond *MetalBond) *metalBondPeer {

	peer := metalBondPeer{
		remoteAddr:        remoteAddr,
		direction:         direction,
		state:             CONNECTING,
		receivedRoutes:    newRouteTable(),
		subscribedVNIs:    make(map[VNI]bool),
		keepaliveInterval: keepaliveInterval,
		metalbond:         metalbond,
	}

	go peer.handle()

	return &peer
}

func (p *metalBondPeer) String() string {
	return p.remoteAddr
}

func (p *metalBondPeer) GetState() ConnectionState {
	p.mtxState.RLock()
	state := p.state
	p.mtxState.RUnlock()
	return state
}

func (p *metalBondPeer) Subscribe(vni VNI) error {
	p.metalbond.log().Infof("addSubscriber(%d)", vni)
	peer := &metalBondPeer{}
	p.metalbond.mtxSubscribers.Lock()

	if _, exists := p.metalbond.subscribers[vni]; !exists {
		p.metalbond.subscribers[vni] = make(map[*metalBondPeer]bool)
	}

	p.metalbond.subscribers[vni][peer] = true
	p.metalbond.mtxSubscribers.Unlock()

	p.metalbond.log().Infof("Subscription to VNI %d", vni)

	return nil
}

func (p *metalBondPeer) Unsubscribe(vni VNI) error {
	if p.direction == INCOMING {
		return fmt.Errorf("cannot unsubscribe on incoming connection")
	}

	msg := msgUnsubscribe{
		VNI: vni,
	}

	return p.sendMessage(msg)
}

func (p *metalBondPeer) SendUpdate(upd msgUpdate) error {
	if p.GetState() != ESTABLISHED {
		return fmt.Errorf("connection not ESTABLISHED")
	}

	if err := p.sendMessage(upd); err != nil {
		p.log().Errorf("Cannot send message: %v", err)
		return err
	}
	return nil
}

///////////////////////////////////////////////////////////////////
//            PRIVATE METHODS BELOW                              //
///////////////////////////////////////////////////////////////////

func (p *metalBondPeer) setState(newState ConnectionState) {
	oldState := p.state

	p.mtxState.Lock()
	p.state = newState
	p.mtxState.Unlock()

	if oldState != newState && newState == ESTABLISHED {
		p.metalbond.mtxMySubscriptions.RLock()
		for sub := range p.metalbond.mySubscriptions {
			if err := p.Subscribe(sub); err != nil {
				p.log().Errorf("Cannot subscribe: %v", err)
			}
		}
		p.metalbond.mtxMySubscriptions.RUnlock()

		rt := p.metalbond.getMyAnnouncements()
		for _, vni := range rt.GetVNIs() {
			for dest, hops := range rt.GetDestinationsByVNI(vni) {
				for _, hop := range hops {

					upd := msgUpdate{
						VNI:         vni,
						Destination: dest,
						NextHop:     hop,
					}

					err := p.SendUpdate(upd)
					if err != nil {
						p.log().Debugf("Could not send update to peer: %v", err)
					}
				}
			}
		}
	}
}

func (p *metalBondPeer) log() *logrus.Entry {
	return logrus.WithField("peer", p.remoteAddr).WithField("state", p.GetState().String())
}

func (p *metalBondPeer) handle() {

	rdb := redis.NewClient(&redis.Options{
		Addr:     p.remoteAddr,
		Password: "", // No password set
		DB:       0,  // Use default DB
	})

	ctx := context.Background()

	if p.redisClient == nil {
		p.redisClient = rdb
	}

	// Server - client
	if p.direction == INCOMING {
		fmt.Println("Entering subscribe loop:", p.remoteAddr)
		pubsub := rdb.PSubscribe(ctx, "vni/*")
		defer pubsub.Close()
		for {
			msg, err := pubsub.ReceiveMessage(ctx)
			if err != nil {
				fmt.Println("Error receiving message:", err)
				break
			}

			// Decode the JSON message
			var obj RouteObjectWithAction
			if err := json.Unmarshal([]byte(msg.Payload), &obj); err != nil {
				fmt.Println("Error decoding JSON:", err)
				continue
			}

			// Get the Redis key by stripping "vni/" from the channel name
			vni := strings.TrimPrefix(msg.Channel, "vni/")

			plainObj, err := json.Marshal(RouteObject{Dest: obj.Dest, Hop: obj.Hop})
			if err != nil {
				fmt.Println("Error encoding object to JSON:", err)
				return
			}

			// Add/Remove the object to the Redis set
			if obj.Action == ADD {
				_, err = rdb.SAdd(ctx, vni, plainObj).Result()
				if err != nil {
					fmt.Println("Error adding object to set:", err)
				} else {
					fmt.Printf("Added object to set '%s': %+v\n", vni, obj)
				}
			} else {
				_, err = rdb.SRem(ctx, vni, plainObj).Result()
				if err != nil {
					fmt.Println("Error removing object to set:", err)
				} else {
					fmt.Printf("Removed object to set '%s': %+v\n", vni, obj)
				}
			}
		}
	}

	// Normal - client
	if p.direction == OUTGOING {
		/*TODO instead of polling the, use the redis client side caching */
		ticker := time.NewTicker(5 * time.Second)
		for range ticker.C {
			for vni, _ := range p.metalbond.subscribers {
				values, err := rdb.SMembers(ctx, strconv.FormatUint(uint64(vni), 10)).Result()
				if err != nil {
					fmt.Println("Error getting set members:", err)
					return
				}
				// Traverse the values and decode the JSON objects
				for _, value := range values {
					var obj RouteObject
					err := json.Unmarshal([]byte(value), &obj)
					if err != nil {
						fmt.Println("Error decoding JSON:", err)
					} else {
						fmt.Printf("Decoded object: %+v\n", obj)
					}
				}
			}
		}
	}
}

func (p *metalBondPeer) Close() {
	if p.GetState() != CLOSED {
		p.setState(CLOSED)
		p.shutdown <- true
		p.keepaliveStop <- true
		close(p.txChan)
	}
}

func (p *metalBondPeer) Reset() {
	if p.GetState() == CLOSED {
		return
	}

	switch p.direction {
	case INCOMING:
		if err := p.metalbond.RemovePeer(p.remoteAddr); err != nil {
			p.log().Errorf("Failed to remove peer: %v", err)
		}
	case OUTGOING:
		p.log().Infof("Resetting connection...")
		p.setState(RETRY)
		close(p.txChan)
		p.shutdown <- true
		p.keepaliveStop <- true
		p.wg.Wait()

		p.log().Infof("Closed. Waiting %s...", RETRY_INTERVAL)

		time.Sleep(RETRY_INTERVAL)
		p.setState(CONNECTING)
		p.log().Infof("Reconnecting...")

		go p.handle()
	}
}

func (p *metalBondPeer) sendMessage(msg message) error {

	return nil
}
