package shared

import "net"

// IsPublicIP reports whether an address is a routable public destination --
// i.e. NOT loopback, private, link-local, multicast, CGNAT/tailnet, or
// otherwise reserved.
//
// It lives here because two different fetchers need the SAME answer and must
// not drift apart: the in-process page/feed/image client checks it in its
// dialer Control hook (server/fetchpage.go, on the already-resolved ip of every
// connection, so redirects and DNS rebinding are covered), while the yt-dlp
// executor can only check it up front on the resolved host (tools/youtube.go) --
// yt-dlp does its own dialling and takes no such hook.
//
// The rule both enforce: a URL that came from a MODEL must never turn our
// server into a proxy into the machine's own network. quartermaster's admin API
// listens on loopback and is not API-key gated, and a LAN box or a cloud
// metadata endpoint is one guessed hostname away.
func IsPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// 100.64.0.0/10 -- tailnet addresses live there, and a tailnet host is
		// exactly as much "someone else's private network" as a LAN one.
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return false
		}
		// 0.0.0.0/8 and 240.0.0.0/4 (reserved) are not routable destinations.
		if v4[0] == 0 || v4[0] >= 240 {
			return false
		}
		return true
	}
	// IPv6: reject unique-local (fc00::/7). IPv4-mapped forms of everything
	// above are already handled by To4() returning non-nil for them.
	if len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc {
		return false
	}
	return true
}
