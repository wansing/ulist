package web

import (
	"net"
	"net/http"
)

// extracts the client's IP address from a request
func ExtractIP(r *http.Request) string {

	// "The HTTP server in this package sets RemoteAddr to an "IP:port" address before invoking a handler", so no error expected here
	var clientIP, _, _ = net.SplitHostPort(r.RemoteAddr)

	// ulist will usually run behind an HTTP reverse proxy. We want to log the real client IP address, not the IP address of the proxy.
	//
	// "NGINX even provides a $proxy_add_x_forwarded_for variable to automatically append $remote_addr to any incoming X-Forwarded-For headers."
	// So we could take the first "X-Forwarded-For" address. However the client could set that header, circumventing fail2ban and blacklisting someone else.
	//
	// Let's use the "X-Real-IP" header instead, which is a single IP address. If you use multiple reverse proxies, only the first one must set this header.
	// nginx example:
	// proxy_set_header X-Real-IP $remote_addr;

	if realIP := r.Header.Get("X-Real-IP"); realIP != "" {
		clientIP = realIP
	}

	return clientIP
}
