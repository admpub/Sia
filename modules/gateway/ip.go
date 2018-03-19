package gateway

import (
	"net"
	"time"

	"github.com/NebulousLabs/Sia/encoding"
	"github.com/NebulousLabs/Sia/modules"
	"github.com/NebulousLabs/errors"
)

// discoverPeerIP is the handler for the discoverPeer RPC. It returns the
// public ip of the caller back to the caller. This allows for peer-to-peer ip
// discovery without centralized services.
func (g *Gateway) discoverPeerIP(conn modules.PeerConn) error {
	conn.SetDeadline(time.Now().Add(connStdDeadline))
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return errors.AddContext(err, "failed to split host from port")
	}
	return encoding.WriteObject(conn, host)
}

// managedIPFromPeers asks the peers the node is connected to for the node's
// public ip address. If not enough peers are available
func (g *Gateway) managedIPFromPeers() (string, error) {
	for {
		// Check for shutdown signal.
		select {
		case <-g.peerTG.StopChan():
			return "", errors.New("interrupted by shutdown")
		default:
		}
		// Get peers
		g.mu.RLock()
		peers := g.Peers()
		g.mu.RUnlock()
		// Check if there are enough peers. Otherwise wait.
		if len(peers) < minPeersForIPDiscovery {
			g.waitForPeerDiscoverySignal()
			continue
		}
		// Ask all the peers about our ip in parallel
		returnChan := make(chan string)
		for _, peer := range peers {
			go g.RPC(peer.NetAddress, "DiscoverIP", func(conn modules.PeerConn) error {
				var address string
				err := encoding.ReadObject(conn, &address, 100)
				if err != nil {
					returnChan <- ""
					g.log.Debugf("DEBUG: failed to receive ip address: %v", err)
					return err
				}
				addr := net.ParseIP(address)
				if addr == nil {
					returnChan <- ""
					g.log.Debug("DEBUG: failed to parse ip address")
					return errors.New("failed to parse ip address")
				}
				returnChan <- addr.String()
				return err
			})
		}
		// Wait for their responses
		addresses := make(map[string]int)
		successfulResponses := 0
		for i := 0; i < len(peers); i++ {
			addr := <-returnChan
			if addr != "" {
				addresses[addr]++
				successfulResponses++
			}
		}
		// If there haven't been enough successful responses we wait some time.
		if successfulResponses < minPeersForIPDiscovery {
			g.waitForPeerDiscoverySignal()
			continue
		}
		// If an address was returned by more than half the peers we consider
		// it valid.
		for addr, count := range addresses {
			if count > successfulResponses/2 {
				g.log.Println("ip successfully discovered using peers:", addr)
				return addr, nil
			}
		}
		// Otherwise we wait before trying again.
		g.waitForPeerDiscoverySignal()
	}
}

// waitForPeerDiscoverySignal blocks for the time specified in
// peerDiscoveryRetryInterval.
func (g *Gateway) waitForPeerDiscoverySignal() {
	select {
	case <-time.After(peerDiscoveryRetryInterval):
	case <-g.peerTG.StopChan():
	}
}