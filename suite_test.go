// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package metalbond

import (
	"net"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	log "github.com/sirupsen/logrus"
)

func TestMetalbond(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Metalbond")
}

var _ = BeforeSuite(func() {
	log.SetLevel(log.TraceLevel)

	SetDefaultEventuallyTimeout(30 * time.Second)
	SetDefaultEventuallyPollingInterval(100 * time.Millisecond)
	SetDefaultConsistentlyDuration(1 * time.Second)
	SetDefaultConsistentlyPollingInterval(100 * time.Millisecond)
})

func getRandomTCPPort() int {
	// create a new TCP listener
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{})

	// check for errors
	if err != nil {
		panic(err)
	}

	// retrieve the port number that was assigned
	port := listener.Addr().(*net.TCPAddr).Port

	// close the listener
	listener.Close()

	// return the randomly generated port number
	return port
}
