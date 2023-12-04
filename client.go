// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package metalbond

type Client interface {
	AddRoute(vni VNI, dest Destination, nexthop NextHop) error
	RemoveRoute(vni VNI, dest Destination, nexthop NextHop) error
}

type DummyClient struct{}

func NewDummyClient() *DummyClient {
	return &DummyClient{}
}

func (c DummyClient) AddRoute(vni VNI, dest Destination, nexthop NextHop) error {
	return nil
}

func (c DummyClient) RemoveRoute(vni VNI, dest Destination, nexthop NextHop) error {
	return nil
}
