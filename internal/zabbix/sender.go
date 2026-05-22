// Package zabbix implements a minimal Zabbix Sender protocol client.
//
// The protocol is documented at
// https://www.zabbix.com/documentation/current/en/manual/appendix/protocols/header_datalen
// and is essentially a JSON payload prefixed with the magic header
// "ZBXD\x01" followed by a little-endian uint64 with the payload length.
package zabbix

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// Metric is a single trapper item value to be delivered to the server.
type Metric struct {
	Host  string `json:"host"`
	Key   string `json:"key"`
	Value string `json:"value"`
	Clock int64  `json:"clock,omitempty"`
}

// NewFloat creates a Metric from a numeric value.
func NewFloat(host, key string, value float64, clock time.Time) Metric {
	return Metric{
		Host:  host,
		Key:   key,
		Value: strconv.FormatFloat(value, 'f', -1, 64),
		Clock: clock.Unix(),
	}
}

// NewInt creates a Metric from an integer value.
func NewInt(host, key string, value int64, clock time.Time) Metric {
	return Metric{
		Host:  host,
		Key:   key,
		Value: strconv.FormatInt(value, 10),
		Clock: clock.Unix(),
	}
}

type request struct {
	Request string   `json:"request"`
	Data    []Metric `json:"data"`
	Clock   int64    `json:"clock,omitempty"`
}

// Response captures the parsed server reply. The "info" field contains a
// human-readable counter like "processed: 4; failed: 0; total: 4; seconds spent: 0.000123".
type Response struct {
	Response string `json:"response"`
	Info     string `json:"info"`
}

// Sender talks to a Zabbix trapper-aware server.
type Sender struct {
	Address string        // "host:port"
	Timeout time.Duration // per-connection timeout (dial + read + write)

	// Dialer is overridable for tests.
	Dialer func(ctx context.Context, address string) (net.Conn, error)
}

// New creates a Sender that connects to address with the given timeout.
func New(address string, timeout time.Duration) *Sender {
	return &Sender{Address: address, Timeout: timeout}
}

// Send transmits the metrics in a single batch and returns the parsed reply.
//
// The server expects a TCP connection per request, so we explicitly close the
// connection at the end.
func (s *Sender) Send(ctx context.Context, metrics []Metric) (*Response, error) {
	if len(metrics) == 0 {
		return nil, errors.New("zabbix: no metrics to send")
	}
	payload, err := encodeRequest(metrics)
	if err != nil {
		return nil, err
	}

	dial := s.Dialer
	if dial == nil {
		d := &net.Dialer{Timeout: s.Timeout}
		dial = func(ctx context.Context, address string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp", address)
		}
	}

	conn, err := dial(ctx, s.Address)
	if err != nil {
		return nil, fmt.Errorf("zabbix dial %s: %w", s.Address, err)
	}
	defer conn.Close()

	if s.Timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(s.Timeout))
	}

	if _, err := conn.Write(payload); err != nil {
		return nil, fmt.Errorf("zabbix write: %w", err)
	}
	resp, err := readResponse(conn)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// encodeRequest builds the binary frame that goes on the wire.
func encodeRequest(metrics []Metric) ([]byte, error) {
	req := request{
		Request: "sender data",
		Data:    metrics,
		Clock:   time.Now().Unix(),
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("zabbix encode: %w", err)
	}
	frame := make([]byte, 0, 13+len(body))
	frame = append(frame, 'Z', 'B', 'X', 'D', 0x01)
	lenBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(lenBuf, uint64(len(body)))
	frame = append(frame, lenBuf...)
	frame = append(frame, body...)
	return frame, nil
}

// readResponse parses the ZBXD-framed reply. We tolerate small implementations
// (Zabbix proxy) that may not return a body — only an error in that case.
func readResponse(r io.Reader) (*Response, error) {
	header := make([]byte, 13)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, fmt.Errorf("zabbix read header: %w", err)
	}
	if string(header[:5]) != "ZBXD\x01" {
		return nil, fmt.Errorf("zabbix: unexpected header %q", header[:5])
	}
	bodyLen := binary.LittleEndian.Uint64(header[5:13])
	// Protect against absurd values from a misbehaving peer.
	if bodyLen > 16<<20 {
		return nil, fmt.Errorf("zabbix: response body too large (%d bytes)", bodyLen)
	}
	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("zabbix read body: %w", err)
	}
	resp := &Response{}
	if err := json.Unmarshal(body, resp); err != nil {
		return nil, fmt.Errorf("zabbix parse response %q: %w", body, err)
	}
	return resp, nil
}
