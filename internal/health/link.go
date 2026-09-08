package health

// Measuring the uplink.
//
// This is the one measurement in the whole panel that is NOT free, and the
// design follows from that.
//
// The kernel's /sys/class/net/<iface>/speed is free but frequently wrong on
// exactly the machines this runs on: a virtio NIC on a VPS reports -1, or
// reports the host's 10 Gbit card while the guest is shaped to 100 Mbit.
// Trusting it would produce recommendations that are confidently wrong.
//
// So the real figure is measured, and measuring means moving bytes. Three
// rules follow:
//
//  1. NEVER automatically. Only when the operator presses the button. On a
//     metered VPS an unrequested speed test costs money, and on a filtered
//     network an unexplained outbound burst is the sort of thing that draws
//     attention — which is the one outcome this whole project is built to
//     avoid.
//  2. Small and bounded. A few megabytes with a hard timeout, not a
//     saturating test. The goal is an order of magnitude — "this is a
//     100 Mbit box, not a 1 Gbit box" — because that is all the
//     recommendations actually need.
//  3. Falls back honestly. If nothing can be reached, it says so rather
//     than inventing a number.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// linkProbes are the endpoints tried, in order.
//
// Chosen to be operated by large CDNs that are reachable from most networks
// and that serve a fixed-size file over plain HTTPS with no API key. They
// are tried in order and the first that works wins; a network that can
// reach none of them gets an honest failure.
var linkProbes = []struct {
	url   string
	bytes int64
}{
	// Cloudflare's own speed-test origin. Serves an exact byte count.
	{"https://speed.cloudflare.com/__down?bytes=10000000", 10_000_000},
	// A widely mirrored fallback.
	{"https://proof.ovh.net/files/10Mb.dat", 1_250_000},
}

// measureTimeout bounds the whole attempt.
//
// Ten seconds is enough to move ten megabytes on anything above about
// 10 Mbit, and short enough that an operator who pressed the button by
// mistake is not stuck waiting.
const measureTimeout = 10 * time.Second

// MeasureLink downloads a small fixed-size file and reports the observed
// throughput in Mbit/s.
//
// Returns the figure and where it came from, or an error when nothing could
// be reached. Never returns a made-up number.
func MeasureLink(ctx context.Context) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, measureTimeout)
	defer cancel()

	client := &http.Client{
		Transport: &http.Transport{
			// A fresh connection every time: reusing one would measure a
			// warm TCP window rather than the link.
			DisableKeepAlives: true,
			DialContext: (&net.Dialer{
				Timeout: 4 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout: 4 * time.Second,
		},
	}

	var lastErr error
	for _, probe := range linkProbes {
		req, err := http.NewRequestWithContext(ctx, "GET", probe.url, nil)
		if err != nil {
			lastErr = err
			continue
		}
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		// Discard as it arrives: buffering ten megabytes to measure it
		// would be a pointless allocation on a 1 GB box.
		n, cErr := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		elapsed := time.Since(start)

		if cErr != nil && n < probe.bytes/2 {
			lastErr = cErr
			continue
		}
		if n < 200_000 || elapsed <= 0 {
			lastErr = fmt.Errorf("only %d bytes arrived", n)
			continue
		}

		// Subtract nothing for setup: including the handshake makes the
		// figure slightly pessimistic, which is the safe direction for a
		// number that sizes buffers.
		mbits := float64(n) * 8 / elapsed.Seconds() / 1e6
		if mbits < 1 {
			mbits = 1
		}
		return int(mbits + 0.5), "measured", nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no probe endpoint could be reached")
	}
	return 0, "failed", lastErr
}
