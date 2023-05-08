package metalbond

import (
	"context"
	"fmt"
	log "github.com/sirupsen/logrus"
	"testing"
	"time"
)

func TestMetalBondPeerReset(t *testing.T) {
	log.SetLevel(log.TraceLevel)
	config := Config{
		KeepaliveInterval: 10,
	}
	client := NewDummyClient()
	metalbondServer := NewMetalBond(config, client)
	serverAddress := "127.0.0.1:4711"
	err := metalbondServer.StartServer(serverAddress)
	if err != nil {
		t.Fatalf("Cannot start server: %v", err)
	}
	metalbondClient := NewMetalBond(config, client)
	if err := metalbondClient.AddPeer(serverAddress); err != nil {
		panic(fmt.Errorf("failed to add server: %v", err))
	}

	// wait max 10 seconds for peer to connect
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Wait for the peer to connect
	err = PollImmediateWithContext(ctx, time.Second, func(ctx context.Context) (bool, error) {
		if len(metalbondServer.peers) > 0 {
			return true, nil
		}
		return false, nil
	})

	// Check if the wait was successful or if it timed out.
	if err != nil {
		t.Error("failed to wait for peer")
	}

	// get the first peer
	var p *metalBondPeer
	for _, peer := range metalbondServer.peers {
		p = peer
		break
	}

	// Check that the peer's state was updated as expected.
	if p.GetState() == CONNECTING {
		t.Errorf("Unexpected state: expected CONNECTING, got %s", p.GetState())
	}

	// call multiple times to check if it panics
	p.Reset()
	p.Reset()
	p.Reset()

	if err := metalbondClient.RemovePeer(serverAddress); err != nil {
		panic(fmt.Errorf("failed to remove server: %v", err))
	}

	// Wait for the peer to disconnect
	err = PollImmediateWithContext(ctx, time.Second, func(ctx context.Context) (bool, error) {
		if len(metalbondServer.peers) == 0 {
			return true, nil
		}
		return false, nil
	})

	// Check if the wait was successful or if it timed out.
	if err != nil {
		t.Error("failed to wait for peer to disconnect")
	}

	// Check that the peer's state was updated as expected.
	if p.GetState() != CLOSED {
		t.Errorf("Unexpected state: expected CLOSED, got %s", p.GetState())
	}

	// Check that the peer's txChan was closed.
	select {
	case _, ok := <-p.txChan:
		if ok {
			t.Errorf("txChan not closed")
		}
	default:
		t.Errorf("txChan not closed")
	}

	// Check that the peer's keepaliveTimer was stopped.
	if p.keepaliveTimer.Stop() {
		t.Errorf("keepaliveTimer was not stopped")
	}
}
