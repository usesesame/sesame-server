package notifications

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"usesesame.app/backend/internal/httpapi"
)

type fakeSMTPServer struct {
	listener    net.Listener
	rejectRcpt  string
	mu          sync.Mutex
	connections int
	delivered   []string
	quits       int
	wait        sync.WaitGroup
}

func startFakeSMTP(t *testing.T, rejectRcpt string) *fakeSMTPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &fakeSMTPServer{listener: listener, rejectRcpt: rejectRcpt}
	server.wait.Add(1)
	go server.accept()
	t.Cleanup(func() {
		_ = listener.Close()
		server.wait.Wait()
	})
	return server
}

func (s *fakeSMTPServer) accept() {
	defer s.wait.Done()
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.connections++
		s.mu.Unlock()
		s.wait.Add(1)
		go s.serve(connection)
	}
}

func (s *fakeSMTPServer) serve(connection net.Conn) {
	defer s.wait.Done()
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(connection)
	reply := func(line string) { _, _ = connection.Write([]byte(line + "\r\n")) }
	reply("220 mail.example.invalid ready")
	recipient := ""
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(command, "EHLO"), strings.HasPrefix(command, "HELO"):
			reply("250 mail.example.invalid")
		case strings.HasPrefix(command, "MAIL FROM:"):
			recipient = ""
			reply("250 ok")
		case strings.HasPrefix(command, "RCPT TO:"):
			recipient = strings.TrimSpace(line)[len("RCPT TO:"):]
			if s.rejectRcpt != "" && strings.Contains(recipient, s.rejectRcpt) {
				reply("550 mailbox unavailable")
				continue
			}
			reply("250 ok")
		case command == "DATA":
			reply("354 go ahead")
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if dataLine == ".\r\n" {
					break
				}
			}
			s.mu.Lock()
			s.delivered = append(s.delivered, recipient)
			s.mu.Unlock()
			reply("250 queued")
		case command == "RSET", command == "NOOP":
			reply("250 ok")
		case command == "QUIT":
			s.mu.Lock()
			s.quits++
			s.mu.Unlock()
			reply("221 bye")
			return
		default:
			reply("502 not implemented")
		}
	}
}

func (s *fakeSMTPServer) counts() (connections, delivered, quits int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connections, len(s.delivered), s.quits
}

func testNotice(to string) httpapi.AccountEmail {
	return httpapi.AccountEmail{Kind: "security-sign-in", To: to, Subject: "New sign-in", Body: "A new sign-in happened.", ExpiresAt: time.Now().Add(time.Hour)}
}

func TestSMTPBatchReusesOneConnection(t *testing.T) {
	server := startFakeSMTP(t, "")
	sender, err := NewSMTPForLocalDevelopment(server.listener.Addr().String(), "Sesame <sender@example.invalid>")
	if err != nil {
		t.Fatalf("new SMTP sender: %v", err)
	}
	batch := sender.OpenBatch()
	for _, to := range []string{"first@example.invalid", "second@example.invalid", "third@example.invalid"} {
		if err := batch.SendAccountEmail(context.Background(), testNotice(to)); err != nil {
			t.Fatalf("send to %s: %v", to, err)
		}
	}
	if err := batch.Close(); err != nil {
		t.Fatalf("close batch: %v", err)
	}
	server.listener.Close()
	server.wait.Wait()
	connections, delivered, quits := server.counts()
	if connections != 1 || delivered != 3 || quits != 1 {
		t.Fatalf("connections=%d delivered=%d quits=%d, want 1, 3 and 1", connections, delivered, quits)
	}
}

func TestSMTPBatchRedialsAfterAFailedMessage(t *testing.T) {
	server := startFakeSMTP(t, "refused@example.invalid")
	sender, err := NewSMTPForLocalDevelopment(server.listener.Addr().String(), "Sesame <sender@example.invalid>")
	if err != nil {
		t.Fatalf("new SMTP sender: %v", err)
	}
	batch := sender.OpenBatch()
	if err := batch.SendAccountEmail(context.Background(), testNotice("first@example.invalid")); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if err := batch.SendAccountEmail(context.Background(), testNotice("refused@example.invalid")); err == nil {
		t.Fatal("a refused recipient was reported as sent")
	}
	if err := batch.SendAccountEmail(context.Background(), testNotice("third@example.invalid")); err != nil {
		t.Fatalf("send after a failure: %v", err)
	}
	if err := batch.Close(); err != nil {
		t.Fatalf("close batch: %v", err)
	}
	server.listener.Close()
	server.wait.Wait()
	connections, delivered, _ := server.counts()
	if connections != 2 || delivered != 2 {
		t.Fatalf("connections=%d delivered=%d, want a fresh connection after the failure and two deliveries", connections, delivered)
	}
}

func TestSMTPBatchRejectsHeaderInjectionWithoutDialing(t *testing.T) {
	server := startFakeSMTP(t, "")
	sender, err := NewSMTPForLocalDevelopment(server.listener.Addr().String(), "Sesame <sender@example.invalid>")
	if err != nil {
		t.Fatalf("new SMTP sender: %v", err)
	}
	batch := sender.OpenBatch()
	if err := batch.SendAccountEmail(context.Background(), testNotice("first@example.invalid\r\nBcc: other@example.invalid")); err == nil {
		t.Fatal("a recipient with a line break was accepted")
	}
	if err := batch.Close(); err != nil {
		t.Fatalf("close batch: %v", err)
	}
	if connections, _, _ := server.counts(); connections != 0 {
		t.Fatalf("connections = %d, want none for a refused message", connections)
	}
}

func TestSMTPBatchRequiresSTARTTLSOutsideLocalCapture(t *testing.T) {
	server := startFakeSMTP(t, "")
	sender, err := NewSMTP(server.listener.Addr().String(), "", "", "Sesame <sender@example.invalid>")
	if err != nil {
		t.Fatalf("new SMTP sender: %v", err)
	}
	batch := sender.OpenBatch()
	err = batch.SendAccountEmail(context.Background(), testNotice("first@example.invalid"))
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("send without STARTTLS error = %v, want a STARTTLS refusal", err)
	}
	_ = batch.Close()
	if _, delivered, _ := server.counts(); delivered != 0 {
		t.Fatalf("delivered = %d over plaintext, want none", delivered)
	}
}
