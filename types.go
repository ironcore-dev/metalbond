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

	"net/netip"

	"github.com/onmetal/metalbond/pb"
	"google.golang.org/protobuf/proto"
)

/////////////////////////////////////////////////////////////
//                           TYPES                         //
/////////////////////////////////////////////////////////////

type VNI uint32

type Destination struct {
	IPVersion IPVersion
	Prefix    netip.Prefix
}

func (d Destination) String() string {
	return d.Prefix.String()
}

type NextHop struct {
	TargetAddress    netip.Addr
	TargetVNI        uint32
	Type             pb.NextHopType
	NATPortRangeFrom uint16
	NATPortRangeTo   uint16
}

type RouteObjectWithAction struct {
	Action UpdateAction
	Dest   Destination
	Hop    NextHop
}

type RouteObject struct {
	Dest Destination
	Hop  NextHop
}

func (h NextHop) String() string {
	if h.TargetVNI != 0 {
		return fmt.Sprintf("%s (VNI: %d)", h.TargetAddress.String(), h.TargetVNI)
	} else {
		return h.TargetAddress.String()
	}
}

/////////////////////////////////////////////////////////////
//                           ENUMS                         //
/////////////////////////////////////////////////////////////

type IPVersion uint8

const (
	IPV4 IPVersion = 4
	IPV6 IPVersion = 6
)

type ConnectionDirection uint8

const (
	INCOMING ConnectionDirection = iota
	OUTGOING
)

type ConnectionState uint8

func (cs ConnectionState) String() string {
	switch cs {
	case CONNECTING:
		return "CONNECTING"
	case HELLO_SENT:
		return "HELLO_SENT"
	case HELLO_RECEIVED:
		return "HELLO_RECEIVED"
	case ESTABLISHED:
		return "ESTABLISHED"
	case RETRY:
		return "RETRY"
	case CLOSED:
		return "CLOSED"
	default:
		return "UNKNOWN"
	}
}

const (
	CONNECTING ConnectionState = iota
	HELLO_SENT
	HELLO_RECEIVED
	ESTABLISHED
	RETRY
	CLOSED
)

type MESSAGE_TYPE uint8

const (
	HELLO       MESSAGE_TYPE = 1
	KEEPALIVE   MESSAGE_TYPE = 2
	SUBSCRIBE   MESSAGE_TYPE = 3
	UNSUBSCRIBE MESSAGE_TYPE = 4
	UPDATE      MESSAGE_TYPE = 5
)

type UpdateAction uint8

const (
	ADD UpdateAction = iota
	REMOVE
)

type message interface {
	Serialize() ([]byte, error)
}

type msgSubscribe struct {
	message
	VNI VNI
}

func (msg msgSubscribe) Serialize() ([]byte, error) {
	pbmsg := pb.Subscription{
		Vni: uint32(msg.VNI),
	}

	msgBytes, err := proto.Marshal(&pbmsg)
	if err != nil {
		return nil, fmt.Errorf("could not marshal message: %v", err)
	}

	if len(msgBytes) > 1188 {
		return nil, fmt.Errorf("message too long: %d bytes > maximum of 1188 bytes", len(msgBytes))
	}

	return msgBytes, nil
}

type msgUnsubscribe struct {
	message
	VNI VNI
}

func (msg msgUnsubscribe) Serialize() ([]byte, error) {
	return nil, fmt.Errorf("NOT IMPLEMENTED")
}

type msgUpdate struct {
	message
	Action      UpdateAction
	VNI         VNI
	Destination Destination
	NextHop     NextHop
}

func (msg msgUpdate) Serialize() ([]byte, error) {
	pbmsg := pb.Update{
		Vni:         uint32(msg.VNI),
		Destination: &pb.Destination{},
		NextHop:     &pb.NextHop{},
	}
	switch msg.Action {
	case ADD:
		pbmsg.Action = pb.Action_ADD
	case REMOVE:
		pbmsg.Action = pb.Action_REMOVE
	default:
		return nil, fmt.Errorf("invalid UPDATE action")
	}

	switch msg.Destination.IPVersion {
	case IPV4:
		pbmsg.Destination.IpVersion = pb.IPVersion_IPv4
	case IPV6:
		pbmsg.Destination.IpVersion = pb.IPVersion_IPv6
	default:
		return nil, fmt.Errorf("invalid Destination IP version")
	}
	pbmsg.Destination.Prefix = msg.Destination.Prefix.Addr().AsSlice()
	pbmsg.Destination.PrefixLength = uint32(msg.Destination.Prefix.Bits())

	pbmsg.NextHop.TargetVNI = msg.NextHop.TargetVNI
	pbmsg.NextHop.TargetAddress = msg.NextHop.TargetAddress.AsSlice()
	pbmsg.NextHop.Type = msg.NextHop.Type

	if pbmsg.NextHop.Type == pb.NextHopType_NAT {
		pbmsg.NextHop.NatPortRangeFrom = uint32(msg.NextHop.NATPortRangeFrom)
		pbmsg.NextHop.NatPortRangeTo = uint32(msg.NextHop.NATPortRangeTo)
	}

	msgBytes, err := proto.Marshal(&pbmsg)
	if err != nil {
		return nil, fmt.Errorf("could not marshal message: %v", err)
	}

	if len(msgBytes) > 1188 {
		return nil, fmt.Errorf("message too long: %d bytes > maximum of 1188 bytes", len(msgBytes))
	}

	return msgBytes, nil
}
