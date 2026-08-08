package agent

import (
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// queryNTP sends a minimal NTP client request to server and returns the
// offset between the local clock and the server's reported time. Uses only
// the standard library (net, encoding/binary, time).
//
// The NTP packet is 48 bytes. The transmit timestamp at bytes 40..47 is
// the server's view of "now" (NTP epoch starts 1900-01-01). We compare to
// our local clock (Unix epoch starts 1970-01-01) and add the
// 70-year offset.
func queryNTP(server string) (time.Duration, error) {
	conn, err := net.DialTimeout("udp", server, 3*time.Second)
	if err != nil {
		return 0, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// LI=0, VN=4, Mode=3 (client). Rest of the 48-byte packet is zero.
	pkt := make([]byte, 48)
	pkt[0] = 0x1B

	beforeSend := time.Now()
	if _, err := conn.Write(pkt); err != nil {
		return 0, fmt.Errorf("write: %w", err)
	}

	resp := make([]byte, 48)
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Read(resp); err != nil {
		return 0, fmt.Errorf("read: %w", err)
	}
	afterRecv := time.Now()

	// Bytes 40..43: transmit timestamp seconds (big-endian uint32).
	// NTP epoch: 1900-01-01. Unix epoch: 1970-01-01. Offset: 2208988800.
	secs := binary.BigEndian.Uint32(resp[40:44])
	frac := binary.BigEndian.Uint32(resp[44:48])
	ntpTime := time.Unix(int64(secs)-2208988800, int64(frac)*1e9>>32)

	// Best estimate of the server time at the moment we received the reply:
	// average of beforeSend and afterRecv (NTP convention).
	localTime := beforeSend.Add(afterRecv.Sub(beforeSend) / 2)
	drift := ntpTime.Sub(localTime)
	return drift, nil
}
