package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// ProxyAddress validates a SOCKS5 endpoint without including URL credentials in errors.
func ProxyAddress(value string) (string, error) {
	address := value
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err != nil {
			return "", fmt.Errorf("invalid SOCKS5 URL")
		}
		if u.Scheme != "socks5" && u.Scheme != "socks5h" {
			return "", fmt.Errorf("only socks5 proxies are supported")
		}
		if u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return "", fmt.Errorf("proxy URL must contain only a host and port")
		}
		address = u.Host
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || strings.ContainsAny(host, "/@\\?#") || strings.ContainsFunc(host, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", fmt.Errorf("proxy must be HOST:PORT or socks5://HOST:PORT")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strings.ContainsFunc(port, func(r rune) bool { return r < '0' || r > '9' }) {
		return "", fmt.Errorf("proxy port must be between 1 and 65535")
	}
	return net.JoinHostPort(host, port), nil
}
