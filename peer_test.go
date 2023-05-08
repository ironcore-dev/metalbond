package metalbond

import (
	"context"
	"fmt"
	log "github.com/sirupsen/logrus"
	"testing"
	"time"
)

const (
	serverAddress = "127.0.0.1:4711"
)

func setup() (*MetalBond, *DummyClient, error) {
	log.SetLevel(log.TraceLevel)
	config := Config{}
	client := NewDummyClient()
	srv := NewMetalBond(config, client)
	err := srv.StartServer(serverAddress)
	if err != nil {
		return nil, nil, err
	}

	return srv, client, err
}

func cleanup(server *MetalBond) {
	server.Shutdown()
}
func TestMetalBondPeerReset(t *testing.T) {

	mbServer, client, err := setup()
	if err != nil {
		panic(fmt.Errorf("failed to setup mbServer: %v", err))
	}
	defer cleanup(mbServer)

	mbClient := NewMetalBond(Config{}, client)
	if err := mbClient.AddPeer(serverAddress); err != nil {
		panic(fmt.Errorf("failed to add mbServer: %v", err))
	}

	// wait max 10 seconds for peer to connect
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Wait for the peer to connect
	err = PollImmediateWithContext(ctx, time.Second, func(ctx context.Context) (bool, error) {
		log.Infof("peers %d", len(mbServer.peers))
		if len(mbServer.peers) > 0 {
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
	for _, peer := range mbServer.peers {
		p = peer
		break
	}

	// Wait for the peer to be established
	err = PollImmediateWithContext(ctx, time.Second, func(ctx context.Context) (bool, error) {
		log.Infof("peer %s", p.GetState())
		if p.GetState() == ESTABLISHED {
			return true, nil
		}
		return false, nil
	})

	// Check if the wait was successful or if it timed out.
	if err != nil {
		t.Error("failed to wait for peer")
	}

	// call multiple times to check if it panics
	p.Reset()
	p.Reset()
	p.Reset()

	if err := mbClient.RemovePeer(serverAddress); err != nil {
		panic(fmt.Errorf("failed to remove mbServer: %v", err))
	}

	// Wait for the peer to disconnect
	err = PollImmediateWithContext(ctx, time.Second, func(ctx context.Context) (bool, error) {
		log.Infof("peers %d", len(mbServer.peers))
		if len(mbServer.peers) == 0 {
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
}

func TestMetalBondPeerReconnect(t *testing.T) {
	mbServer, client, err := setup()
	if err != nil {
		panic(fmt.Errorf("failed to setup mbServer: %v", err))
	}
	defer cleanup(mbServer)

	mbClient := NewMetalBond(Config{}, client)
	if err := mbClient.AddPeer(serverAddress); err != nil {
		panic(fmt.Errorf("failed to add server: %v", err))
	}

	// wait max 10 seconds for peer to connect
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Wait for the peer to connect
	err = PollImmediateWithContext(ctx, time.Second, func(ctx context.Context) (bool, error) {
		log.Infof("peers %d", len(mbServer.peers))
		if len(mbServer.peers) > 0 {
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
	for _, peer := range mbServer.peers {
		p = peer
		break
	}

	// Wait for the peer to be established
	err = PollImmediateWithContext(ctx, time.Second, func(ctx context.Context) (bool, error) {
		log.Infof("peer %s", p.GetState())
		if p.GetState() == ESTABLISHED {
			return true, nil
		}
		return false, nil
	})

	// Check if the wait was successful or if it timed out.
	if err != nil {
		t.Error("failed to wait for peer")
	}

	// reset peer
	p.Reset()

	// Wait for the peer to disconnect
	err = PollImmediateWithContext(ctx, time.Second, func(ctx context.Context) (bool, error) {
		log.Infof("peers %d", len(mbServer.peers))
		if len(mbServer.peers) == 0 {
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

	// Wait for the peer to reconnect
	err = PollImmediateWithContext(ctx, time.Second, func(ctx context.Context) (bool, error) {
		log.Infof("peers %d", len(mbServer.peers))
		if len(mbServer.peers) > 0 {
			return true, nil
		}
		return false, nil
	})

	// Check if the wait was successful or if it timed out.
	if err != nil {
		t.Error("failed to wait for peer")
	}

	for _, peer := range mbServer.peers {
		p = peer
		break
	}

	// Wait for the peer to be established
	err = PollImmediateWithContext(ctx, time.Second, func(ctx context.Context) (bool, error) {
		log.Infof("peer %s", p.GetState())
		if p.GetState() == ESTABLISHED {
			return true, nil
		}
		return false, nil
	})

	// Check if the wait was successful or if it timed out.
	if err != nil {
		t.Error("failed to wait for peer")
	}
}
