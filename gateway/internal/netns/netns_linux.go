//go:build linux

// Package netns configures a network namespace that belongs to one sandbox: the interfaces it
// has, its address, its default route, and the routes that refuse what the workspace was not
// granted.
//
// It talks to the kernel directly rather than shelling out to `ip`, for two reasons. The route
// list is a security boundary that lives in code, where a test can read it and a missing
// binary cannot silently skip it (the old script swallowed `ip`'s errors), and `iproute2` is
// one less thing a machine has to install before a workspace can run (ADR 0026).
//
// Every call here needs CAP_NET_ADMIN in the namespace it runs in, which the caller has by
// construction: this runs as the first process inside pasta's namespace, owned by a user
// namespace of its own, on a machine where nobody is root.
package netns

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"syscall"
)

// Netlink's own numbers. syscall carries the socket family and protocol; the message types and
// the attribute kinds are not in the standard library.
const (
	rtmNewLink  = 16
	rtmNewAddr  = 20
	rtmNewRoute = 24

	nlmsgError = 2

	nlmFRequest = 0x1
	nlmFAck     = 0x4
	nlmFCreate  = 0x400
	nlmFExcl    = 0x200

	// What one address attribute can be: IPv4 says IFA_LOCAL, IPv6 says IFA_ADDRESS. Setting
	// both for IPv4 is what makes the address show up in `ip addr` rather than only in the
	// kernel's idea of the peer.
	ifaAddress = 1
	ifaLocal   = 2

	// What one route attribute can be.
	rtaDst     = 1
	rtaOIF     = 4
	rtaGateway = 5

	rtTableMain = 254
	rtProtoBoot = 3
	// The route types, in the kernel's order: unicast, local, broadcast, anycast, multicast,
	// blackhole, unreachable. Unreachable is seven, not two.
	rtnUnicast = 1
	rtnUnreach = 7

	rtScopeUniverse = 0
	iffUp           = 0x1
)

// native is the byte order netlink speaks: the host's.
var native = binary.NativeEndian

// Up brings one interface up. It is how `lo` and the tap pasta makes become usable, and it is
// idempotent: a namespace whose interface is already up is not an error.
func Up(name string) error {
	index, err := interfaceIndex(name)
	if err != nil {
		return err
	}
	payload := make([]byte, 16)
	// Family zero (unspecified), one byte of padding, then the interface's own type (zero:
	// "whatever it is"), index, flags, and the flags this message is allowed to change.
	native.PutUint32(payload[4:], uint32(index))
	native.PutUint32(payload[8:], iffUp)
	native.PutUint32(payload[12:], iffUp)
	return request(message(rtmNewLink, nlmFRequest|nlmFAck, payload))
}

// Address gives an interface one address, with the connected route the kernel makes with it.
// That route is what keeps the sandbox's own gateway reachable when the ranges around it are
// refused.
func Address(name, cidr string) error {
	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("netns: %q is not an address with a prefix: %w", cidr, err)
	}
	index, err := interfaceIndex(name)
	if err != nil {
		return err
	}
	family, address, err := familyAndBytes(ip)
	if err != nil {
		return err
	}
	ones, _ := network.Mask.Size()
	payload := make([]byte, 8)
	payload[0] = family
	payload[1] = byte(ones)
	payload[2] = 0 // flags
	payload[3] = rtScopeUniverse
	native.PutUint32(payload[4:], uint32(index))

	body := append(attribute(ifaLocal, address), attribute(ifaAddress, address)...)
	err = request(message(rtmNewAddr, nlmFRequest|nlmFAck|nlmFCreate|nlmFExcl, append(payload, body...)))
	if errors.Is(err, syscall.EEXIST) {
		return nil
	}
	return err
}

// Gateway installs the default route through one address. It has to come after Address: the
// kernel will not accept a gateway it has no connected route to.
func Gateway(name, gateway string) error {
	index, err := interfaceIndex(name)
	if err != nil {
		return err
	}
	ip := net.ParseIP(gateway)
	if ip == nil {
		return fmt.Errorf("netns: %q is not an address", gateway)
	}
	family, bytes, err := familyAndBytes(ip)
	if err != nil {
		return err
	}
	payload := routeMessage(family, 0, rtnUnicast)
	body := append(attribute(rtaGateway, bytes), uint32Attribute(rtaOIF, uint32(index))...)
	err = request(message(rtmNewRoute, nlmFRequest|nlmFAck|nlmFCreate|nlmFExcl, append(payload, body...)))
	if errors.Is(err, syscall.EEXIST) {
		return nil
	}
	return err
}

// Unreachable refuses one range. The route says "unreachable" rather than dropping silently,
// so a program inside the sandbox gets an error immediately instead of waiting for a timeout.
// A packet that matches it never leaves the sandbox: there is no destination to reach and no
// interface to leave by.
func Unreachable(cidr string) error {
	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("netns: %q is not a range: %w", cidr, err)
	}
	family, bytes, err := familyAndBytes(ip)
	if err != nil {
		return err
	}
	ones, _ := network.Mask.Size()
	payload := routeMessage(family, byte(ones), rtnUnreach)
	body := attribute(rtaDst, bytes)
	err = request(message(rtmNewRoute, nlmFRequest|nlmFAck|nlmFCreate|nlmFExcl, append(payload, body...)))
	if errors.Is(err, syscall.EEXIST) {
		return nil
	}
	return err
}

// routeMessage is the fixed part of a route: a destination prefix length, the main table,
// boot's protocol, and the route's type. The main table is the one a resolver or a browser
// looks at, which is why nothing here names a table explicitly.
func routeMessage(family byte, prefix byte, kind byte) []byte {
	payload := make([]byte, 12)
	payload[0] = family
	payload[1] = prefix
	payload[4] = rtTableMain
	payload[5] = rtProtoBoot
	payload[6] = rtScopeUniverse
	payload[7] = kind
	return payload
}

// message frames one netlink request: a header, then the payload, padded to the four-byte
// boundary the kernel parses on.
func message(kind uint16, flags uint16, payload []byte) []byte {
	const header = 16
	frame := make([]byte, align4(header+len(payload)))
	native.PutUint32(frame[0:], uint32(header+len(payload)))
	native.PutUint16(frame[4:], kind)
	native.PutUint16(frame[6:], flags)
	native.PutUint32(frame[8:], 1) // sequence
	copy(frame[header:], payload)
	return frame
}

// attribute is one type/length/data triple, padded the same way.
func attribute(kind uint16, data []byte) []byte {
	length := 4 + len(data)
	attribute := make([]byte, align4(length))
	native.PutUint16(attribute[0:], uint16(length))
	native.PutUint16(attribute[2:], kind)
	copy(attribute[4:], data)
	return attribute
}

func uint32Attribute(kind uint16, value uint32) []byte {
	data := make([]byte, 4)
	native.PutUint32(data, value)
	return attribute(kind, data)
}

func align4(n int) int { return (n + 3) &^ 3 }

// request sends one message and reads the kernel's answer. Netlink answers every request that
// asked for an acknowledgement with an error message whose code is zero on success, so the
// whole of "did this work" is one 32-bit field.
func request(frame []byte) error {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_ROUTE)
	if err != nil {
		return fmt.Errorf("netns: cannot open a netlink socket: %w", err)
	}
	defer syscall.Close(fd)
	if err := syscall.Sendto(fd, frame, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return fmt.Errorf("netns: %w", err)
	}
	// Netlink answers in messages that may share a datagram, and an ACK can arrive after other
	// notifications: read until one of them is the ACK, and refuse only what the kernel refused.
	buffer := make([]byte, 4096)
	for {
		n, _, err := syscall.Recvfrom(fd, buffer, 0)
		if err != nil {
			return fmt.Errorf("netns: %w", err)
		}
		acked, err := readMessages(buffer[:n])
		if err != nil || acked {
			return err
		}
	}
}

// readMessages walks one datagram's messages. It answers whether one of them was the ACK: a
// datagram of notifications only is not the end of the wait.
func readMessages(payload []byte) (acked bool, err error) {
	for offset := 0; offset+16 <= len(payload); {
		length := int(native.Uint32(payload[offset:]))
		kind := native.Uint16(payload[offset+4:])
		if length < 16 || offset+length > len(payload) {
			return false, errors.New("netns: the kernel's answer is not a netlink message")
		}
		if kind == nlmsgError && length >= 20 {
			if code := int32(native.Uint32(payload[offset+16:])); code != 0 {
				return false, syscall.Errno(-code)
			}
			return true, nil
		}
		offset += align4(length)
	}
	return false, nil
}

func interfaceIndex(name string) (int, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return 0, fmt.Errorf("netns: no interface %q here: %w", name, err)
	}
	return iface.Index, nil
}

// familyAndBytes is the address as the kernel wants it: a family, and four bytes for IPv4 or
// sixteen for IPv6.
func familyAndBytes(ip net.IP) (byte, []byte, error) {
	if v4 := ip.To4(); v4 != nil {
		return syscall.AF_INET, v4, nil
	}
	if v6 := ip.To16(); v6 != nil {
		return syscall.AF_INET6, v6, nil
	}
	return 0, nil, fmt.Errorf("netns: %q is not an IP address", ip)
}
