package sshclient

import "time"

// A negative request reply still proves that the peer is responsive. Servers
// need not implement the OpenSSH extension to benefit from this health check.
func (c *Client) keepAlive(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
		}
		result := make(chan error, 1)
		go func() { _, _, err := c.client.SendRequest("keepalive@openssh.com", true, nil); result <- err }()
		timer := time.NewTimer(c.timeout)
		select {
		case err := <-result:
			timer.Stop()
			if err != nil {
				_ = c.Close()
				return
			}
		case <-c.done:
			timer.Stop()
			<-result
			return
		case <-timer.C:
			_ = c.Close()
			<-result
			return
		}
	}
}
