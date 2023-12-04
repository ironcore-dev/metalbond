// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package metalbond

import (
	"fmt"

	"net/netip"

	"github.com/ironcore-dev/metalbond/pb"
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

var _ message = (*msgHello)(nil)

type msgHello struct {
	KeepaliveInterval uint32
	IsServer          bool
}

func (m msgHello) Serialize() ([]byte, error) {
	pbmsg := pb.Hello{
		KeepaliveInterval: m.KeepaliveInterval,
		IsServer:          m.IsServer,
	}

	msgBytes, err := proto.Marshal(&pbmsg)
	if err != nil {
		return nil, fmt.Errorf("Could not marshal message: %v", err)
	}

	if len(msgBytes) > 1188 {
		return nil, fmt.Errorf("Message too long: %d bytes > maximum of 1188 bytes", len(msgBytes))
	}

	return msgBytes, nil
}

var _ message = (*msgKeepalive)(nil)

type msgKeepalive struct {
}

func (msg msgKeepalive) Serialize() ([]byte, error) {
	return []byte{}, nil
}

var _ message = (*msgSubscribe)(nil)

type msgSubscribe struct {
	VNI VNI
}

func (msg msgSubscribe) Serialize() ([]byte, error) {
	pbmsg := pb.Subscription{
		Vni: uint32(msg.VNI),
	}

	msgBytes, err := proto.Marshal(&pbmsg)
	if err != nil {
		return nil, fmt.Errorf("Could not marshal message: %v", err)
	}

	if len(msgBytes) > 1188 {
		return nil, fmt.Errorf("Message too long: %d bytes > maximum of 1188 bytes", len(msgBytes))
	}

	return msgBytes, nil
}

var _ message = (*msgUnsubscribe)(nil)

type msgUnsubscribe struct {
	VNI VNI
}

func (msg msgUnsubscribe) Serialize() ([]byte, error) {
	pbmsg := pb.Subscription{
		Vni: uint32(msg.VNI),
	}

	msgBytes, err := proto.Marshal(&pbmsg)
	if err != nil {
		return nil, fmt.Errorf("Could not marshal message: %v", err)
	}

	if len(msgBytes) > 1188 {
		return nil, fmt.Errorf("Message too long: %d bytes > maximum of 1188 bytes", len(msgBytes))
	}

	return msgBytes, nil
}

var _ message = (*msgUpdate)(nil)

type msgUpdate struct {
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
		return nil, fmt.Errorf("Invalid UPDATE action")
	}

	switch msg.Destination.IPVersion {
	case IPV4:
		pbmsg.Destination.IpVersion = pb.IPVersion_IPv4
	case IPV6:
		pbmsg.Destination.IpVersion = pb.IPVersion_IPv6
	default:
		return nil, fmt.Errorf("Invalid Destination IP version")
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
		return nil, fmt.Errorf("Could not marshal message: %v", err)
	}

	if len(msgBytes) > 1188 {
		return nil, fmt.Errorf("Message too long: %d bytes > maximum of 1188 bytes", len(msgBytes))
	}

	return msgBytes, nil
}

func deserializeHelloMsg(pktBytes []byte) (*msgHello, error) {
	pbmsg := &pb.Hello{}
	if err := proto.Unmarshal(pktBytes, pbmsg); err != nil {
		return nil, fmt.Errorf("Cannot unmarshal received packet. Closing connection: %v", err)
	}

	return &msgHello{
		KeepaliveInterval: pbmsg.KeepaliveInterval,
	}, nil
}

func deserializeSubscribeMsg(pktBytes []byte) (*msgSubscribe, error) {
	pbmsg := &pb.Subscription{}
	if err := proto.Unmarshal(pktBytes, pbmsg); err != nil {
		return nil, fmt.Errorf("Cannot unmarshal received packet. Closing connection: %v", err)
	}

	return &msgSubscribe{
		VNI: VNI(pbmsg.Vni),
	}, nil
}

func deserializeUnsubscribeMsg(pktBytes []byte) (*msgUnsubscribe, error) {
	pbmsg := &pb.Subscription{}
	if err := proto.Unmarshal(pktBytes, pbmsg); err != nil {
		return nil, fmt.Errorf("Cannot unmarshal received packet. Closing connection: %v", err)
	}

	return &msgUnsubscribe{
		VNI: VNI(pbmsg.Vni),
	}, nil
}

func deserializeUpdateMsg(pktBytes []byte) (*msgUpdate, error) {
	pbmsg := &pb.Update{}
	if err := proto.Unmarshal(pktBytes, pbmsg); err != nil {
		return nil, fmt.Errorf("Cannot unmarshal received packet. Closing connection: %v", err)
	}

	action := ADD
	if pbmsg.Action == pb.Action_REMOVE {
		action = REMOVE
	}

	if pbmsg.Destination == nil {
		return nil, fmt.Errorf("Destination is nil")
	}

	ipversion := IPV6
	if pbmsg.Destination.IpVersion == pb.IPVersion_IPv4 {
		ipversion = IPV4
	}

	destIP, ok := netip.AddrFromSlice(pbmsg.Destination.Prefix)
	if !ok {
		return nil, fmt.Errorf("Invalid destination IP")
	}
	destination := Destination{
		IPVersion: ipversion,
		Prefix:    netip.PrefixFrom(destIP, int(pbmsg.Destination.PrefixLength)),
	}

	nhAddr, ok := netip.AddrFromSlice(pbmsg.NextHop.TargetAddress)
	if !ok {
		return nil, fmt.Errorf("Invalid nexthop IP")
	}
	nexthop := NextHop{
		TargetAddress:    nhAddr,
		TargetVNI:        pbmsg.NextHop.TargetVNI,
		Type:             pbmsg.NextHop.Type,
		NATPortRangeFrom: uint16(pbmsg.NextHop.NatPortRangeFrom),
		NATPortRangeTo:   uint16(pbmsg.NextHop.NatPortRangeTo),
	}

	return &msgUpdate{
		Action:      action,
		VNI:         VNI(pbmsg.Vni),
		Destination: destination,
		NextHop:     nexthop,
	}, nil
}
