// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package spoofer

import (
	"fmt"
	"net"
	"sync"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcap"
	log "github.com/sirupsen/logrus"
)

// ARPSpoofer holds the state for ARP spoofing
type ARPSpoofer struct {
	iface     string
	handle    *pcap.Handle
	mac       net.HardwareAddr
	ipNetwork *net.IPNet
	stopChan  chan struct{}
	wg        *sync.WaitGroup
}

// NewARPSpoofer creates a new ARP spoofer for the given interface and IP prefix
func NewARPSpoofer(iface, ipPrefix string, wg *sync.WaitGroup) (*ARPSpoofer, error) {
	// Parse IP prefix
	_, ipNet, err := net.ParseCIDR(ipPrefix)
	if err != nil {
		return nil, fmt.Errorf("invalid IP prefix: %v", err)
	}

	// Get interface info
	netIface, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, fmt.Errorf("interface not found: %v", err)
	}

	// Open pcap handle for the interface
	handle, err := pcap.OpenLive(iface, 65536, true, pcap.BlockForever)
	if err != nil {
		return nil, fmt.Errorf("failed to open interface: %v", err)
	}

	// Set BPF filter to only capture ARP packets
	if err := handle.SetBPFFilter("arp"); err != nil {
		handle.Close()
		return nil, fmt.Errorf("failed to set BPF filter: %v", err)
	}

	return &ARPSpoofer{
		iface:     iface,
		handle:    handle,
		mac:       netIface.HardwareAddr,
		ipNetwork: ipNet,
		stopChan:  make(chan struct{}),
		wg:        wg,
	}, nil
}

// Start begins the ARP spoofing process
func (spoofer *ARPSpoofer) Start() {
	log.Infof("Starting ARP spoofing on interface %s for IP range %s", spoofer.iface, spoofer.ipNetwork.String())
	log.Infof("Using MAC address: %s", spoofer.mac.String())

	spoofer.wg.Add(1) // Add this line

	go func(wg *sync.WaitGroup, handle *pcap.Handle) {
		stop := false

		defer handle.Close()
		defer wg.Done()
		packetSource := gopacket.NewPacketSource(handle, handle.LinkType())

		for !stop {
			select {
			case <-spoofer.stopChan:
				stop = true
			case packet := <-packetSource.Packets():
				spoofer.handlePacket(packet)
			}
		}
	}(spoofer.wg, spoofer.handle)
}

func (spoofer *ARPSpoofer) handlePacket(packet gopacket.Packet) {
	arpLayer := packet.Layer(layers.LayerTypeARP)
	if arpLayer == nil {
		return
	}

	arp := arpLayer.(*layers.ARP)
	if arp.Operation != layers.ARPRequest {
		return
	}

	targetIP := net.IP(arp.DstProtAddress)

	if !spoofer.ipNetwork.Contains(targetIP) {
		return
	}

	eth := layers.Ethernet{
		SrcMAC:       spoofer.mac,
		DstMAC:       net.HardwareAddr(arp.SourceHwAddress),
		EthernetType: layers.EthernetTypeARP,
	}

	arpReply := layers.ARP{
		AddrType:          arp.AddrType,
		Protocol:          arp.Protocol,
		HwAddressSize:     arp.HwAddressSize,
		ProtAddressSize:   arp.ProtAddressSize,
		Operation:         layers.ARPReply,
		SourceHwAddress:   spoofer.mac,
		SourceProtAddress: arp.DstProtAddress,
		DstHwAddress:      arp.SourceHwAddress,
		DstProtAddress:    arp.SourceProtAddress,
	}

	buffer := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{
		FixLengths:       true,
		ComputeChecksums: true,
	}

	if err := gopacket.SerializeLayers(buffer, opts, &eth, &arpReply); err != nil {
		log.Errorf("Failed to serialize ARP reply: %v", err)
		return
	}

	if err := spoofer.handle.WritePacketData(buffer.Bytes()); err != nil {
		log.Errorf("Failed to send ARP reply: %v", err)
		return
	}

	log.Debugf("Sent ARP reply for %s to %s",
		net.IP(arp.DstProtAddress).String(),
		net.IP(arp.SourceProtAddress).String())
}

func (spoofer *ARPSpoofer) Stop() {
	spoofer.stopChan <- struct{}{}
}
