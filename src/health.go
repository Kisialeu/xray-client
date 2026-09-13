package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/proxy"
)

var healthURLs = []string{"https://www.gstatic.com/generate_204", "https://cp.cloudflare.com/generate_204"}

func (c *Client) checkConnectivity(ctx context.Context) error {
	inbound := c.InboundProxy()
	dialer, err := proxy.SOCKS5("tcp", inbound.String(), nil, &net.Dialer{Timeout: 5 * time.Second})
	if err != nil {
		return err
	}
	transport := &http.Transport{DialContext: dialer.(proxy.ContextDialer).DialContext, DisableKeepAlives: true, TLSHandshakeTimeout: 5 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	for _, target := range healthURLs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNoContent && readErr == nil {
			return nil
		}
	}
	return fmt.Errorf("authenticated proxy connectivity check failed")
}

func (c *Client) monitorSession(ctx context.Context) error {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-c.TunnelDone():
			return err
		case <-ticker.C:
			if err := c.checkConnectivity(ctx); err != nil {
				failures++
				if failures >= 3 {
					return err
				}
			} else {
				failures = 0
			}
		}
	}
}
