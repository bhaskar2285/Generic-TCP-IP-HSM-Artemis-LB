package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Azure/go-amqp"
	"github.com/google/uuid"
)

// Config holds all runtime parameters, mirroring application.properties fields.
type Config struct {
	// TCP listener
	TCPPort        int
	MaxConnections int
	ReadTimeout    time.Duration

	// AMQP broker
	BrokerURLs  []string // primary + failover
	SelfQueue   string   // where responses arrive (unique per instance)
	OutboundQ   string   // where requests go (LB reads from here)

	// Prometheus
	MetricsPort int
}

// pending tracks an in-flight TCP request waiting for an AMQP reply.
type pending struct {
	ch      chan []byte
	created time.Time
}

// Bridge ties together the TCP listener and the AMQP session.
type Bridge struct {
	cfg Config

	mu      sync.Mutex
	waiters map[string]*pending
	sender  *amqp.Sender
}

func newBridge(cfg Config) *Bridge {
	return &Bridge{
		cfg:     cfg,
		waiters: make(map[string]*pending),
	}
}

// ── TCP side ─────────────────────────────────────────────────────────────────

func (b *Bridge) serveTCP(ctx context.Context) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", b.cfg.TCPPort))
	if err != nil {
		return fmt.Errorf("tcp listen: %w", err)
	}
	defer ln.Close()
	log.Printf("TCP listening on :%d", b.cfg.TCPPort)

	sem := make(chan struct{}, b.cfg.MaxConnections)
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				log.Printf("accept error: %v", err)
				continue
			}
		}
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			b.handleConn(ctx, conn)
		}()
	}
}

func (b *Bridge) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	for {
		if b.cfg.ReadTimeout > 0 {
			conn.SetReadDeadline(time.Now().Add(b.cfg.ReadTimeout))
		}
		frame, err := readFrame(conn)
		if err != nil {
			if err != io.EOF {
				log.Printf("read frame: %v", err)
			}
			return
		}

		corrID := uuid.NewString()
		ch := make(chan []byte, 1)
		b.mu.Lock()
		b.waiters[corrID] = &pending{ch: ch, created: time.Now()}
		pendingCount := len(b.waiters)
		b.mu.Unlock()

		cmd := ""
		if len(frame) >= 6 {
			cmd = string(frame[4:6])
		}
		t0 := time.Now()
		log.Printf("REQ corrID=%s cmd=%s req_bytes=%d addr=%s pending=%d", corrID, cmd, len(frame), conn.RemoteAddr(), pendingCount)

		b.enqueue(ctx, corrID, frame)
		enqueueMs := time.Since(t0).Milliseconds()

		select {
		case reply := <-ch:
			elapsed := time.Since(t0)
			queueWaitMs := elapsed.Milliseconds() - enqueueMs
			respCmd := ""
			if len(reply) >= 6 {
				respCmd = string(reply[4:6])
			}
			ec := ""
			if len(reply) >= 8 {
				ec = string(reply[6:8])
			}
			if elapsed.Milliseconds() >= 1000 {
				log.Printf("SLOW corrID=%s cmd=%s->%s ec=%s total_ms=%d enqueue_ms=%d queue_wait_ms=%d resp_bytes=%d addr=%s",
					corrID, cmd, respCmd, ec, elapsed.Milliseconds(), enqueueMs, queueWaitMs, len(reply), conn.RemoteAddr())
			} else {
				log.Printf("REPLY corrID=%s cmd=%s->%s ec=%s total_ms=%d enqueue_ms=%d queue_wait_ms=%d resp_bytes=%d",
					corrID, cmd, respCmd, ec, elapsed.Milliseconds(), enqueueMs, queueWaitMs, len(reply))
			}
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := writeFrame(conn, reply); err != nil {
				log.Printf("write frame: %v", err)
			}
		case <-time.After(b.cfg.ReadTimeout):
			b.mu.Lock()
			delete(b.waiters, corrID)
			b.mu.Unlock()
			log.Printf("TIMEOUT corrID=%s cmd=%s total_ms=%d enqueue_ms=%d addr=%s", corrID, cmd, time.Since(t0).Milliseconds(), enqueueMs, conn.RemoteAddr())
			return
		case <-ctx.Done():
			return
		}
	}
}

// ── AMQP side ─────────────────────────────────────────────────────────────────

// runAMQP connects (with retry) and runs sender + receiver until ctx cancelled.
func (b *Bridge) runAMQP(ctx context.Context) {
	for {
		err := b.amqpSession(ctx)
		if ctx.Err() != nil {
			return
		}
		log.Printf("AMQP session ended (%v), reconnecting in 2s", err)
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

func (b *Bridge) amqpSession(ctx context.Context) error {
	// try brokers in order (failover)
	var conn *amqp.Conn
	var err error
	for _, url := range b.cfg.BrokerURLs {
		conn, err = amqp.Dial(ctx, url, nil)
		if err == nil {
			break
		}
		log.Printf("AMQP dial %s: %v", url, err)
	}
	if conn == nil {
		return fmt.Errorf("all brokers unreachable")
	}
	defer conn.Close()
	log.Printf("AMQP connected to %s", b.cfg.BrokerURLs[0])

	sess, err := conn.NewSession(ctx, nil)
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}

	// Sender → outbound queue. Plain address — FQQN is for receivers only.
	// Artemis routes to anycast queue by address name.
	sender, err := sess.NewSender(ctx, b.cfg.OutboundQ, &amqp.SenderOptions{
		SettlementMode: amqp.SenderSettleModeUnsettled.Ptr(),
		TargetCapabilities: []string{"queue"},
	})
	if err != nil {
		return fmt.Errorf("new sender: %w", err)
	}
	defer sender.Close(ctx)

	// Receiver ← self queue (responses arrive here).
	// FQQN forces anycast addressing; SourceCapabilities "queue" tells Artemis
	// to create/bind the address as ANYCAST so JMS senders can deliver to it.
	inFQQN := b.cfg.SelfQueue + "::" + b.cfg.SelfQueue
	receiver, err := sess.NewReceiver(ctx, inFQQN, &amqp.ReceiverOptions{
		Credit:             200,
		SourceCapabilities: []string{"queue"},
	})
	if err != nil {
		return fmt.Errorf("new receiver: %w", err)
	}
	defer receiver.Close(ctx)

	log.Printf("AMQP ready: outbound=%s inbound=%s", b.cfg.OutboundQ, b.cfg.SelfQueue)

	// Store sender for use by TCP goroutines
	b.mu.Lock()
	b.sender = sender
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.sender = nil
		b.mu.Unlock()
	}()

	// Receive loop
	for {
		msg, err := receiver.Receive(ctx, nil)
		if err != nil {
			return fmt.Errorf("receive: %w", err)
		}
		receiver.AcceptMessage(ctx, msg)

		// LB echoes ip_connectionId (which we set to our corrID) in the reply.
		// Fall back to AMQP CorrelationID if the property is absent.
		corrID := ""
		if v, ok := msg.ApplicationProperties["ip_connectionId"]; ok {
			corrID, _ = v.(string)
		}
		if corrID == "" {
			corrID, _ = msg.Properties.CorrelationID.(string)
		}
		body := flattenBody(msg)
		if corrID == "" {
			log.Printf("RECV dropping: no corrID props=%v", msg.ApplicationProperties)
			continue
		}

		b.mu.Lock()
		p, ok := b.waiters[corrID]
		if ok {
			delete(b.waiters, corrID)
		}
		b.mu.Unlock()

		if ok {
			p.ch <- body
		}
	}
}

// enqueue sends a TCP frame to the outbound AMQP queue.
func (b *Bridge) enqueue(ctx context.Context, corrID string, frame []byte) {
	b.mu.Lock()
	s := b.sender
	b.mu.Unlock()
	if s == nil {
		log.Printf("no AMQP sender, dropping corrID=%s", corrID)
		return
	}
	msg := &amqp.Message{
		Data: [][]byte{frame},
		Properties: &amqp.MessageProperties{
			MessageID:     corrID,
			CorrelationID: corrID,
			ReplyTo:       &b.cfg.SelfQueue,
		},
		// ip_connectionId is the Spring Integration header the LB reads and echoes back.
		// gw_replyTo is the string fallback the LB uses if JMSReplyTo is absent.
		ApplicationProperties: map[string]any{
			"ip_connectionId": corrID,
			"gw_replyTo":      b.cfg.SelfQueue,
		},
	}
	sendCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := s.Send(sendCtx, msg, nil); err != nil {
		log.Printf("AMQP send error corrID=%s: %v", corrID, err)
	}
}

// ── Wire helpers ──────────────────────────────────────────────────────────────

func readFrame(r io.Reader) ([]byte, error) {
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint16(lenBuf[:])
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

func writeFrame(w io.Writer, body []byte) error {
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(body)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

func flattenBody(msg *amqp.Message) []byte {
	var out []byte
	for _, seg := range msg.Data {
		out = append(out, seg...)
	}
	return out
}

// ── Metrics ───────────────────────────────────────────────────────────────────

func (b *Bridge) serveMetrics(port int) {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		pending := len(b.waiters)
		b.mu.Unlock()
		fmt.Fprintf(w, "# HELP eznet_pending_requests In-flight requests awaiting AMQP reply\n")
		fmt.Fprintf(w, "# TYPE eznet_pending_requests gauge\n")
		fmt.Fprintf(w, "eznet_pending_requests %d\n", pending)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	addr := fmt.Sprintf(":%d", port)
	log.Printf("metrics on %s", addr)
	http.ListenAndServe(addr, mux)
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	tcpPort     := flag.Int("tcp-port", 9100, "TCP listen port for HSM clients")
	maxConn     := flag.Int("max-conn", 200, "Max concurrent TCP connections")
	readTimeout := flag.Duration("read-timeout", 30*time.Second, "TCP read/reply timeout")
	broker1     := flag.String("broker", "amqp://artemis-master:61618", "Primary AMQP broker URL")
	broker2     := flag.String("broker2", "amqp://artemis-slave:61618", "Failover AMQP broker URL")
	selfQueue   := flag.String("self-queue", "hsm-transparent-lb-inbound-1", "Queue this instance receives replies on")
	outboundQ   := flag.String("outbound-queue", "hsm.transparent.lb.in", "Queue LB reads requests from")
	metricsPort := flag.Int("metrics-port", 8120, "HTTP metrics/health port")
	logFile     := flag.String("log-file", "", "Path to log file (stdout if empty)")
	flag.Parse()

	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatalf("open log file %s: %v", *logFile, err)
		}
		log.SetOutput(f)
	}

	cfg := Config{
		TCPPort:        *tcpPort,
		MaxConnections: *maxConn,
		ReadTimeout:    *readTimeout,
		BrokerURLs:     []string{*broker1, *broker2},
		SelfQueue:      *selfQueue,
		OutboundQ:      *outboundQ,
		MetricsPort:    *metricsPort,
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("go-eznet starting: tcp=%d amqp=%v self=%s out=%s",
		cfg.TCPPort, cfg.BrokerURLs, cfg.SelfQueue, cfg.OutboundQ)

	b := newBridge(cfg)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go b.serveMetrics(cfg.MetricsPort)
	go b.runAMQP(ctx)

	if err := b.serveTCP(ctx); err != nil {
		log.Printf("TCP server: %v", err)
		os.Exit(1)
	}
}
