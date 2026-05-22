package zabbix

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// startFakeServer launches an in-process TCP server that speaks the Zabbix
// trapper protocol. The provided handler receives the decoded payload and
// returns the body to send back; the framing is handled automatically.
func startFakeServer(t *testing.T, handler func([]Metric) (string, string)) (addr string, received chan []Metric, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	received = make(chan []Metric, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		header := make([]byte, 13)
		if _, err := io.ReadFull(conn, header); err != nil {
			return
		}
		if string(header[:5]) != "ZBXD\x01" {
			return
		}
		bodyLen := binary.LittleEndian.Uint64(header[5:13])
		body := make([]byte, bodyLen)
		if _, err := io.ReadFull(conn, body); err != nil {
			return
		}
		var req request
		_ = json.Unmarshal(body, &req)
		received <- req.Data

		respStatus, info := handler(req.Data)
		respBody, _ := json.Marshal(Response{Response: respStatus, Info: info})
		out := make([]byte, 0, 13+len(respBody))
		out = append(out, 'Z', 'B', 'X', 'D', 0x01)
		lenBuf := make([]byte, 8)
		binary.LittleEndian.PutUint64(lenBuf, uint64(len(respBody)))
		out = append(out, lenBuf...)
		out = append(out, respBody...)
		_, _ = conn.Write(out)
	}()
	return ln.Addr().String(), received, func() {
		ln.Close()
		<-done
	}
}

func TestSender_Send_DeliversMetrics(t *testing.T) {
	addr, recv, stop := startFakeServer(t, func(m []Metric) (string, string) {
		return "success", "processed: 2; failed: 0; total: 2; seconds spent: 0.000123"
	})
	defer stop()

	s := New(addr, 2*time.Second)
	now := time.Unix(1700000000, 0)
	resp, err := s.Send(context.Background(), []Metric{
		NewInt("host1", "pg.backup.status", 1, now),
		NewFloat("host1", "pg.backup.duration", 12.5, now),
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if resp.Response != "success" {
		t.Errorf("unexpected response status: %q", resp.Response)
	}
	if !strings.Contains(resp.Info, "processed: 2") {
		t.Errorf("unexpected info: %q", resp.Info)
	}

	got := <-recv
	if len(got) != 2 {
		t.Fatalf("expected 2 metrics, got %d", len(got))
	}
	if got[0].Key != "pg.backup.status" || got[0].Value != "1" {
		t.Errorf("metric 0 wrong: %+v", got[0])
	}
	if got[1].Key != "pg.backup.duration" || got[1].Value != "12.5" {
		t.Errorf("metric 1 wrong: %+v", got[1])
	}
	if got[0].Clock != now.Unix() {
		t.Errorf("clock not propagated: %d", got[0].Clock)
	}
}

func TestSender_Send_EmptyMetricsRejected(t *testing.T) {
	s := New("127.0.0.1:1", time.Second)
	if _, err := s.Send(context.Background(), nil); err == nil {
		t.Fatal("expected error for empty metrics")
	}
}

func TestEncodeRequest_HeaderLayout(t *testing.T) {
	now := time.Unix(42, 0)
	frame, err := encodeRequest([]Metric{NewInt("h", "k", 5, now)})
	if err != nil {
		t.Fatal(err)
	}
	if string(frame[:5]) != "ZBXD\x01" {
		t.Fatalf("bad magic: %q", frame[:5])
	}
	bodyLen := binary.LittleEndian.Uint64(frame[5:13])
	if int(bodyLen) != len(frame)-13 {
		t.Errorf("body length mismatch: header says %d, got %d", bodyLen, len(frame)-13)
	}
	var req request
	if err := json.Unmarshal(frame[13:], &req); err != nil {
		t.Fatalf("body not valid json: %v", err)
	}
	if req.Request != "sender data" {
		t.Errorf("bad request type: %q", req.Request)
	}
	if len(req.Data) != 1 || req.Data[0].Key != "k" {
		t.Errorf("payload mismatch: %+v", req.Data)
	}
}
