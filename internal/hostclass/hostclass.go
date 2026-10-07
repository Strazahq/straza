// Package hostclass names the classes of address a request from strazad
// should not reach, for the drafts check that reads a proposed address and
// the manager's dial guard alike. It contacts nothing.
package hostclass

import "net/netip"

// The classes of an address a request from strazad should not reach: its
// own machine, a private network, a link-local address, or a cloud's
// metadata service, in the words of the sentences that quote a class.
const (
	Loopback    = "loopback"
	Unspecified = "unspecified"
	Private     = "private"
	LinkLocal   = "link-local"
	Metadata    = "cloud metadata"
)

var (
	// metadataAddrs are the cloud metadata services that no address class
	// names: AWS's IPv6 endpoint, which sits in the private range, and
	// Alibaba's, which sits in the shared address space. 169.254.169.254
	// is here too, so it reads as metadata rather than link-local.
	metadataAddrs = map[netip.Addr]bool{
		netip.MustParseAddr("169.254.169.254"): true,
		netip.MustParseAddr("fd00:ec2::254"):   true,
		netip.MustParseAddr("100.100.100.200"): true,
	}
	// sharedAddrs is the carrier-grade shared address space, which the
	// standard library does not count as private but no public server uses.
	sharedAddrs = netip.MustParsePrefix("100.64.0.0/10")
	// nat64 and sixToFour are the IPv6 prefixes that carry an IPv4 address
	// a gateway forwards to: the NAT64 well-known prefix and 6to4.
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour = netip.MustParsePrefix("2002::/16")
)

// Addr names the class of addr, reading an IPv4-mapped IPv6 address as its
// IPv4 address first, or answers "".
func Addr(addr netip.Addr) string {
	addr = addr.Unmap().WithZone("")
	switch {
	case metadataAddrs[addr]:
		return Metadata
	case addr.IsLoopback():
		return Loopback
	case addr.IsUnspecified():
		return Unspecified
	case addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast():
		return LinkLocal
	case addr.IsPrivate() || sharedAddrs.Contains(addr):
		return Private
	}
	return ""
}

// Refused names the class of addr that strazad does not dial for a server:
// loopback, unspecified, link-local or cloud metadata, reading an
// IPv4-mapped IPv6 address as its IPv4 address first, and a NAT64
// (64:ff9b::/96) or 6to4 (2002::/16) address as the IPv4 address it
// embeds, since a gateway forwards it there, or answers "". A private
// address passes, because a server may run on the operator's own network.
func Refused(addr netip.Addr) string {
	a := addr.Unmap().WithZone("").As16()
	switch {
	case addr.Is6() && nat64.Contains(addr.WithZone("")):
		addr = netip.AddrFrom4([4]byte(a[12:16]))
	case addr.Is6() && sixToFour.Contains(addr.WithZone("")):
		addr = netip.AddrFrom4([4]byte(a[2:6]))
	}
	if c := Addr(addr); c != Private {
		return c
	}
	return ""
}
