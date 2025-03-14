// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/ironcore-dev/metalbond/extra/arp_spoofer/spoofer"
	log "github.com/sirupsen/logrus"
)

var cli struct {
	Interface string `help:"Network interface for ARP spoofing (e.g., eth0)"`
	IPPrefix  string `help:"IP prefix to spoof ARP responses for (e.g., 192.168.1.0/24)"`
	Version   bool   `help:"Print version information and quit"`
}

var version = "unknown"
var wg sync.WaitGroup

func main() {
	var arpSpoofer *spoofer.ARPSpoofer
	var err error

	kong.Parse(&cli)

	if cli.Version {
		log.Infof("version: %s", version)
		return
	}

	if cli.Interface != "" && cli.IPPrefix != "" {
		log.Infof("Initializing ARP spoofing on interface %s for IP range %s",
			cli.Interface, cli.IPPrefix)

		arpSpoofer, err = spoofer.NewARPSpoofer(cli.Interface, cli.IPPrefix, &wg)
		if err != nil {
			log.Warnf("Failed to initialize ARP spoofing: %v", err)
			return
		} else {
			arpSpoofer.Start()
		}
	} else {
		log.Errorf("No interface or IP prefix specified.")
		return
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)
	<-done

	if arpSpoofer != nil {
		arpSpoofer.Stop()
		wg.Wait()
	}

	log.Infof("Exited")

}
