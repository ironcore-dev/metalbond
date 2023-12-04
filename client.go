// Copyright 2022 IronCore authors
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
