package metalbond

import (
	"net/netip"
	"time"

	"github.com/ironcore-dev/metalbond/pb"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RouteTable", func() {
	var (
		rt          routeTable
		vni         VNI
		dest        Destination
		nextHopNorm NextHop
		nextHopNAT  NextHop
		peer1       *metalBondPeer
		peer2       *metalBondPeer
	)

	BeforeEach(func() {
		rt = newRouteTable()
		vni = VNI(100)

		dest = Destination{
			Prefix:    netip.MustParsePrefix("192.168.1.0/24"),
			IPVersion: IPV4,
		}

		nextHopNorm = NextHop{
			TargetAddress: netip.MustParseAddr("192.168.1.1"),
			TargetVNI:     0,
			Type:          pb.NextHopType_STANDARD,
		}

		nextHopNAT = NextHop{
			TargetAddress:    netip.MustParseAddr("192.168.1.2"),
			TargetVNI:        0,
			Type:             pb.NextHopType_NAT,
			NATPortRangeFrom: 1000,
			NATPortRangeTo:   2000,
		}

		peer1 = &metalBondPeer{}
		peer2 = &metalBondPeer{}
	})

	Describe("deriveIPVersion", func() {
		It("should return IPV4 for an IPv4 prefix", func() {
			prefix := netip.MustParsePrefix("10.0.0.0/8")
			Expect(deriveIPVersion(prefix)).To(Equal(IPV4))
		})

		It("should return IPV6 for an IPv6 prefix", func() {
			prefix := netip.MustParsePrefix("2001:db8::/64")
			Expect(deriveIPVersion(prefix)).To(Equal(IPV6))
		})
	})

	Describe("AddNextHop", func() {
		It("should add a NORMAL-type next hop successfully", func() {
			err := rt.AddNextHop(vni, dest, nextHopNorm, peer1)
			Expect(err).ToNot(HaveOccurred())
			Expect(rt.NextHopExists(vni, dest, nextHopNorm, peer1)).To(BeTrue())
		})

		It("should add a NAT-type next hop successfully", func() {
			err := rt.AddNextHop(vni, dest, nextHopNAT, peer1)
			Expect(err).ToNot(HaveOccurred())
			Expect(rt.NextHopExists(vni, dest, nextHopNAT, peer1)).To(BeTrue())
		})

		It("should not add the same next hop twice for the same peer", func() {
			err := rt.AddNextHop(vni, dest, nextHopNorm, peer1)
			Expect(err).ToNot(HaveOccurred())

			err = rt.AddNextHop(vni, dest, nextHopNorm, peer1)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Nexthop already exists"))
		})

		It("should allow the same next hop for a different peer", func() {
			err := rt.AddNextHop(vni, dest, nextHopNorm, peer1)
			Expect(err).ToNot(HaveOccurred())

			err = rt.AddNextHop(vni, dest, nextHopNorm, peer2)
			Expect(err).ToNot(HaveOccurred())

			Expect(rt.NextHopExists(vni, dest, nextHopNorm, peer1)).To(BeTrue())
			Expect(rt.NextHopExists(vni, dest, nextHopNorm, peer2)).To(BeTrue())
		})

		It("should add multiple next hops for the same destination", func() {
			err := rt.AddNextHop(vni, dest, nextHopNorm, peer1)
			Expect(err).ToNot(HaveOccurred())

			err = rt.AddNextHop(vni, dest, nextHopNAT, peer1)
			Expect(err).ToNot(HaveOccurred())

			Expect(rt.NextHopExists(vni, dest, nextHopNorm, peer1)).To(BeTrue())
			Expect(rt.NextHopExists(vni, dest, nextHopNAT, peer1)).To(BeTrue())
		})
	})

	Describe("RemoveNextHop", func() {
		It("should remove a next hop successfully with single peer reference", func() {
			_ = rt.AddNextHop(vni, dest, nextHopNorm, peer1)

			err, remaining := rt.RemoveNextHop(vni, dest, nextHopNorm, peer1)
			Expect(err).ToNot(HaveOccurred())
			Expect(remaining).To(Equal(0))
			Expect(rt.NextHopExists(vni, dest, nextHopNorm, peer1)).To(BeFalse())
		})

		It("should remove a single peer from a multi-peer next hop", func() {
			_ = rt.AddNextHop(vni, dest, nextHopNorm, peer1)
			_ = rt.AddNextHop(vni, dest, nextHopNorm, peer2)

			err, remaining := rt.RemoveNextHop(vni, dest, nextHopNorm, peer1)
			Expect(err).ToNot(HaveOccurred())
			Expect(remaining).To(Equal(1))

			Expect(rt.NextHopExists(vni, dest, nextHopNorm, peer1)).To(BeFalse())
			// Peer2 should still exist
			Expect(rt.NextHopExists(vni, dest, nextHopNorm, peer2)).To(BeTrue())
		})

		It("should clean up data if the last peer on a next hop is removed", func() {
			_ = rt.AddNextHop(vni, dest, nextHopNorm, peer1)

			err, remaining := rt.RemoveNextHop(vni, dest, nextHopNorm, peer1)
			Expect(err).ToNot(HaveOccurred())
			Expect(remaining).To(Equal(0))

			// All references removed => route table should be empty
			Expect(rt.GetVNIs()).To(BeEmpty())
			Expect(rt.NextHopExists(vni, dest, nextHopNorm, peer1)).To(BeFalse())
		})

		It("should return error if VNI does not exist", func() {
			err, _ := rt.RemoveNextHop(200, dest, nextHopNorm, peer1)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("VNI does not exist"))
		})

		It("should return error if destination does not exist", func() {
			_ = rt.AddNextHop(vni, dest, nextHopNorm, peer1)
			otherDest := Destination{Prefix: netip.MustParsePrefix("10.0.0.0/24"), IPVersion: IPV4}

			err, _ := rt.RemoveNextHop(vni, otherDest, nextHopNorm, peer1)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Destination does not exist"))
		})

		It("should return error if next hop does not exist", func() {
			_ = rt.AddNextHop(vni, dest, nextHopNorm, peer1)
			nonExistentHop := NextHop{
				TargetAddress: netip.MustParseAddr("192.168.99.99"),
				Type:          pb.NextHopType_STANDARD,
			}
			err, _ := rt.RemoveNextHop(vni, dest, nonExistentHop, peer1)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Nexthop does not exist"))
		})

		It("should return error if peer does not exist", func() {
			_ = rt.AddNextHop(vni, dest, nextHopNorm, peer1)
			err, _ := rt.RemoveNextHop(vni, dest, nextHopNorm, peer2)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ReceivedFrom does not exist"))
		})
	})

	Describe("GetDestinationsByVNI", func() {
		It("should return empty map if VNI does not exist", func() {
			result := rt.GetDestinationsByVNI(vni)
			Expect(result).To(BeEmpty())
		})

		It("should return correct destinations by VNI for IPv4", func() {
			_ = rt.AddNextHop(vni, dest, nextHopNorm, peer1)
			result := rt.GetDestinationsByVNI(vni)
			Expect(result).To(HaveKey(dest))
			Expect(result[dest]).To(ContainElement(nextHopNorm))
		})

		It("should return correct destinations by VNI for IPv6", func() {
			ipv6VNI := VNI(999)
			ipv6Dest := Destination{
				Prefix:    netip.MustParsePrefix("2001:db8::/64"),
				IPVersion: IPV6,
			}
			ipv6Hop := NextHop{
				TargetAddress: netip.MustParseAddr("2001:db8::1"),
				Type:          pb.NextHopType_STANDARD,
			}
			Expect(rt.AddNextHop(ipv6VNI, ipv6Dest, ipv6Hop, peer1)).To(Succeed())

			result := rt.GetDestinationsByVNI(ipv6VNI)
			Expect(result).To(HaveKey(ipv6Dest))
			Expect(result[ipv6Dest]).To(ContainElement(ipv6Hop))
		})

		It("should return multiple destinations if configured", func() {
			dest2 := Destination{
				Prefix:    netip.MustParsePrefix("192.168.2.0/24"),
				IPVersion: IPV4,
			}
			_ = rt.AddNextHop(vni, dest, nextHopNorm, peer1)
			_ = rt.AddNextHop(vni, dest2, nextHopNorm, peer1)

			result := rt.GetDestinationsByVNI(vni)
			Expect(result).To(HaveLen(2))
			Expect(result).To(HaveKey(dest))
			Expect(result).To(HaveKey(dest2))
		})
	})

	Describe("GetDestinationsByVNIWithPeer", func() {
		It("should return empty map if VNI does not exist", func() {
			result := rt.GetDestinationsByVNIWithPeer(vni)
			Expect(result).To(BeEmpty())
		})

		It("should return destinations with next hops and peers", func() {
			_ = rt.AddNextHop(vni, dest, nextHopNorm, peer1)
			_ = rt.AddNextHop(vni, dest, nextHopNorm, peer2)
			_ = rt.AddNextHop(vni, dest, nextHopNAT, peer1)

			result := rt.GetDestinationsByVNIWithPeer(vni)
			Expect(result).To(HaveKey(dest))

			// Check nextHopNorm's peers
			Expect(result[dest]).To(HaveKey(nextHopNorm))
			Expect(result[dest][nextHopNorm]).To(ContainElements(peer1, peer2))

			// Check nextHopNAT's peers
			Expect(result[dest]).To(HaveKey(nextHopNAT))
			Expect(result[dest][nextHopNAT]).To(ContainElement(peer1))
		})
	})

	Describe("GetNextHopsByDestination", func() {
		It("should return empty slice if destination does not exist", func() {
			result := rt.GetNextHopsByDestination(vni, dest)
			Expect(result).To(BeEmpty())
		})

		It("should return next hops by destination", func() {
			_ = rt.AddNextHop(vni, dest, nextHopNorm, peer1)
			result := rt.GetNextHopsByDestination(vni, dest)
			Expect(result).To(ContainElement(nextHopNorm))
		})

		It("should handle multiple next hops for the same destination", func() {
			_ = rt.AddNextHop(vni, dest, nextHopNorm, peer1)
			_ = rt.AddNextHop(vni, dest, nextHopNAT, peer1)

			result := rt.GetNextHopsByDestination(vni, dest)
			Expect(result).To(ConsistOf(nextHopNorm, nextHopNAT))
		})
	})

	Describe("GetVNIs", func() {
		It("should return empty list if no VNIs exist", func() {
			Expect(rt.GetVNIs()).To(BeEmpty())
		})

		It("should return list of VNIs after adding routes", func() {
			_ = rt.AddNextHop(vni, dest, nextHopNorm, peer1)
			vnis := rt.GetVNIs()
			Expect(vnis).To(ContainElement(vni))
			Expect(vnis).To(HaveLen(1))
		})

		It("should return multiple VNIs if configured", func() {
			vni2 := VNI(200)
			dest2 := Destination{
				Prefix:    netip.MustParsePrefix("10.0.0.0/24"),
				IPVersion: IPV4,
			}

			_ = rt.AddNextHop(vni, dest, nextHopNorm, peer1)
			_ = rt.AddNextHop(vni2, dest2, nextHopNorm, peer2)

			vnis := rt.GetVNIs()
			Expect(vnis).To(ConsistOf(vni, vni2))
		})
	})

	Describe("NextHopExists", func() {
		It("should return false if next hop does not exist", func() {
			exists := rt.NextHopExists(vni, dest, nextHopNorm, peer1)
			Expect(exists).To(BeFalse())
		})

		It("should return true if next hop exists with peer", func() {
			_ = rt.AddNextHop(vni, dest, nextHopNorm, peer1)
			exists := rt.NextHopExists(vni, dest, nextHopNorm, peer1)
			Expect(exists).To(BeTrue())
		})

		It("should return false if next hop exists but peer is different", func() {
			_ = rt.AddNextHop(vni, dest, nextHopNorm, peer1)
			exists := rt.NextHopExists(vni, dest, nextHopNorm, peer2)
			Expect(exists).To(BeFalse())
		})
	})

	// concurrency test
	Describe("Concurrency safety", func() {
		It("should allow concurrent reads and writes without data corruption", func(done Done) {
			// Fill route table with some data
			for i := 0; i < 10; i++ {
				go func() {
					d := Destination{
						Prefix:    netip.MustParsePrefix("192.168.10.0/24"),
						IPVersion: IPV4,
					}
					h := NextHop{
						TargetAddress: netip.MustParseAddr("192.168.10.1"),
						Type:          pb.NextHopType_STANDARD,
					}
					_ = rt.AddNextHop(vni, d, h, peer1)
				}()
			}

			// Concurrently read from route table
			for i := 0; i < 10; i++ {
				go func() {
					_ = rt.GetDestinationsByVNI(vni)
				}()
			}

			// Let it run briefly
			time.Sleep(100 * time.Millisecond)

			// If no race/panic => success
			close(done)
		}, 1.0) // Timeout after 1 second
	})
})
