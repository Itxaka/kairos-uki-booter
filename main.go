package main

import (
	"errors"
	"fmt"
	kairosTypes "github.com/kairos-io/kairos-sdk/types"
	"github.com/kairos-io/netboot/dhcp4"
	"net"
	"strings"
)

// Offers is a map of GUID to the first efi that was served
// This is used to check if the client was served the first efi
// and if so, we can serve the second efi
type Offers map[string]bool

func main() {
	// Server starts a netboot server which takes over and start to serve off booting in the same network
	// It doesn't need any special configuration, however, requires binding to low ports.
	var offers = Offers{}
	log := kairosTypes.NewKairosLogger("bootserver", "debug", false)
	conn, err := dhcp4.NewSnooperConn("")
	if err != nil {
		log.Error("Error creating DHCP connection: %s", err)
		return
	}
	defer conn.Close()
	log.Info("Listening for DHCP packets")
	for {
		pkt, intf, err := conn.RecvDHCP()
		if err != nil {
			log.Error("Error receiving DHCP packet: %s", err)
			continue
		}
		log.Debugf("Received packet from %s on %s", pkt.HardwareAddr, intf.Name)

		if isBootDHCP(pkt) != nil {
			log.Debug("Ignoring packet from %s (%s): %s", pkt.HardwareAddr, intf.Name, err)
			continue
		}
		log.Infof("Booting %s on %s", pkt.HardwareAddr, intf.Name)

		guid := pkt.Options[dhcp4.OptUidGuidClientIdentifier]
		log.Infof("Client GUID: %x", guid)
		serverIP, err := interfaceIP(intf)
		if err != nil {
			log.Infof("Want to boot %s on %s, but couldn't get a source address: %s", pkt.HardwareAddr, intf.Name, err)
			continue
		}

		resp, err := createOfferDHCP(pkt, serverIP, log)
		if err != nil {
			log.Infof("Failed to construct ProxyDHCP offer for %s: %s", pkt.HardwareAddr, err)
			continue
		}
		// Check if this client was served the first efi
		// We store them by GUID
		// Check if the guid is already in the offers list
		if _, alreadyServed := offers[string(guid)]; alreadyServed {
			log.Infof("Client %s was already served the first efi, serving the second one", pkt.HardwareAddr)
			resp.Options[67] = []byte(fmt.Sprintf("http://%s/kairos.efi", serverIP))
		} else {
			log.Infof("Client %s was not served the first efi, serving the first one", pkt.HardwareAddr)
			resp.Options[67] = []byte(fmt.Sprintf("http://%s/booter.efi", serverIP))
			offers[string(guid)] = true
		}

		if err = conn.SendDHCP(resp, intf); err != nil {
			log.Info("Failed to send ProxyDHCP offer for %s: %s", pkt.HardwareAddr, err)
			continue
		}

		// Now store the guid of the client

	}
}

func isBootDHCP(pkt *dhcp4.Packet) error {
	if pkt.Type != dhcp4.MsgDiscover {
		return fmt.Errorf("packet is %s, not %s", pkt.Type, dhcp4.MsgDiscover)
	}

	if pkt.Options[dhcp4.OptClientSystem] == nil {
		return errors.New("not a PXE boot request (missing option 93)")
	}
	return nil
}

func interfaceIP(intf *net.Interface) (net.IP, error) {
	addrs, err := intf.Addrs()
	if err != nil {
		return nil, err
	}

	// Try to find an IPv4 address to use, in the following order:
	// global unicast (includes rfc1918), link-local unicast,
	// loopback.
	fs := []func(net.IP) bool{
		net.IP.IsGlobalUnicast,
		net.IP.IsLinkLocalUnicast,
		net.IP.IsLoopback,
	}
	for _, f := range fs {
		for _, a := range addrs {
			ipaddr, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipaddr.IP.To4()
			if ip == nil {
				continue
			}
			if f(ip) {
				return ip, nil
			}
		}
	}

	return nil, errors.New("no usable unicast address configured on interface")
}

func createOfferDHCP(pkt *dhcp4.Packet, serverIP net.IP, log kairosTypes.KairosLogger) (*dhcp4.Packet, error) {
	resp := &dhcp4.Packet{
		Type:          dhcp4.MsgOffer,
		TransactionID: pkt.TransactionID,
		Broadcast:     true,
		HardwareAddr:  pkt.HardwareAddr,
		RelayAddr:     pkt.RelayAddr,
		ServerAddr:    serverIP,
		Options:       make(dhcp4.Options),
	}
	resp.Options[dhcp4.OptServerIdentifier] = serverIP

	// Check the vendor-class-identifier
	vendorClass := string(pkt.Options[dhcp4.OptVendorIdentifier])
	log.Debugf("Vendor class identifier: %s", vendorClass)

	switch {
	case strings.Contains(vendorClass, "HTTPClient"):
		log.Debug("Handling HTTP client")
		resp.Options[dhcp4.OptVendorIdentifier] = []byte("HTTPClient")
	default:
		return nil, fmt.Errorf("unknown vendor class identifier: %s", vendorClass)
	}

	return resp, nil
}
