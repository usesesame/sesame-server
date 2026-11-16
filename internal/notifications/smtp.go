package notifications

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"usesesame.app/backend/internal/httpapi"
)

// Tokens appear only in the outbound message; never logged or stored in plaintext.
type SMTP struct {
	address             string
	host                string
	username            string
	password            string
	from                string
	envelopeFrom        string
	tlsConfig           *tls.Config
	allowPlaintextLocal bool
}

func NewSMTP(address, username, password, from string) (*SMTP, error) {
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil || host == "" {
		return nil, errors.New("SMTP address must be host:port")
	}
	parsedFrom, parseErr := mail.ParseAddress(from)
	if strings.TrimSpace(from) == "" || strings.ContainsAny(from, "\r\n") || parseErr != nil {
		return nil, errors.New("SMTP from address is invalid")
	}
	return &SMTP{address: address, host: host, username: username, password: password, from: from, envelopeFrom: parsedFrom.Address}, nil
}

// Explicit, unauthenticated local-only capture mode; production must negotiate STARTTLS.
func NewSMTPForLocalDevelopment(address, from string) (*SMTP, error) {
	sender, err := NewSMTP(address, "", "", from)
	if err != nil {
		return nil, err
	}
	sender.allowPlaintextLocal = true
	return sender, nil
}

func NewSMTPWithTLSConfig(address, username, password, from string, tlsConfig *tls.Config) (*SMTP, error) {
	s, err := NewSMTP(address, username, password, from)
	if err != nil {
		return nil, err
	}
	s.tlsConfig = tlsConfig.Clone()
	return s, nil
}

func (s *SMTP) SendAccountEmail(ctx context.Context, message httpapi.AccountEmail) error {
	raw, err := s.composeAccountEmail(message)
	if err != nil {
		return err
	}
	session, err := s.dial(ctx)
	if err != nil {
		return err
	}
	defer session.close()
	if err := session.send(s.envelopeFrom, message.To, raw); err != nil {
		return err
	}
	return session.client.Quit()
}

func (s *SMTP) OpenBatch() EmailBatch {
	return &smtpBatch{sender: s}
}

func (s *SMTP) composeAccountEmail(message httpapi.AccountEmail) ([]byte, error) {
	if strings.ContainsAny(message.To, "\r\n") || strings.ContainsAny(message.ActionURL, "\r\n") || strings.ContainsAny(message.Subject, "\r\n") || strings.ContainsAny(message.Body, "\r\n") {
		return nil, errors.New("account email contains an invalid header value")
	}
	subject, purpose := accountCopy(message.Kind)
	if message.Subject != "" && message.Body != "" {
		subject, purpose = message.Subject, message.Body
	}
	if subject == "" {
		return nil, errors.New("unsupported account email kind")
	}
	body := fmt.Sprintf("%s\r\n\r\n%s\r\n\r\nThis link expires at %s. If you did not request it, ignore this email.\r\n\r\nSesame never asks for your vault password, recovery kit, TOTP seeds, or backup codes.\r\n",
		purpose, message.ActionURL, message.ExpiresAt.UTC().Format(time.RFC1123))
	return []byte("From: " + s.from + "\r\n" +
		"To: " + message.To + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n" + body), nil
}

const smtpCommandTimeout = 10 * time.Second

type smtpSession struct {
	connection net.Conn
	client     *smtp.Client
}

func (s *SMTP) dial(ctx context.Context) (*smtpSession, error) {
	dialer := &net.Dialer{Timeout: smtpCommandTimeout}
	connection, err := dialer.DialContext(ctx, "tcp", s.address)
	if err != nil {
		return nil, err
	}
	_ = connection.SetDeadline(time.Now().Add(smtpCommandTimeout))
	client, err := smtp.NewClient(connection, s.host)
	if err != nil {
		connection.Close()
		return nil, err
	}
	session := &smtpSession{connection: connection, client: client}
	if ok, _ := client.Extension("STARTTLS"); ok {
		tlsConfig := s.tlsConfig
		if tlsConfig == nil {
			tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: s.host}
		} else {
			if tlsConfig.MinVersion == 0 {
				tlsConfig.MinVersion = tls.VersionTLS12
			}
			if tlsConfig.ServerName == "" {
				tlsConfig.ServerName = s.host
			}
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			session.close()
			return nil, err
		}
	} else if !s.allowPlaintextLocal {
		session.close()
		return nil, errors.New("SMTP server does not offer STARTTLS")
	}
	if s.username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.username, s.password, s.host)); err != nil {
			session.close()
			return nil, err
		}
	}
	return session, nil
}

func (session *smtpSession) send(from, to string, raw []byte) error {
	_ = session.connection.SetDeadline(time.Now().Add(smtpCommandTimeout))
	if err := session.client.Mail(from); err != nil {
		return err
	}
	if err := session.client.Rcpt(to); err != nil {
		return err
	}
	writer, err := session.client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(raw); err != nil {
		writer.Close()
		return err
	}
	return writer.Close()
}

func (session *smtpSession) close() {
	session.client.Close()
	session.connection.Close()
}

type smtpBatch struct {
	sender  *SMTP
	session *smtpSession
}

func (batch *smtpBatch) SendAccountEmail(ctx context.Context, message httpapi.AccountEmail) error {
	raw, err := batch.sender.composeAccountEmail(message)
	if err != nil {
		return err
	}
	if batch.session == nil {
		session, err := batch.sender.dial(ctx)
		if err != nil {
			return err
		}
		batch.session = session
	}
	if err := batch.session.send(batch.sender.envelopeFrom, message.To, raw); err != nil {
		batch.drop()
		return err
	}
	return nil
}

func (batch *smtpBatch) Close() error {
	if batch.session == nil {
		return nil
	}
	_ = batch.session.connection.SetDeadline(time.Now().Add(smtpCommandTimeout))
	err := batch.session.client.Quit()
	batch.drop()
	return err
}

func (batch *smtpBatch) drop() {
	if batch.session != nil {
		batch.session.close()
		batch.session = nil
	}
}

func accountCopy(kind string) (string, string) {
	switch kind {
	case "verify-email":
		return "Verify your Sesame account email", "Open this link to verify your Sesame beta account:"
	case "recover-password":
		return "Reset your Sesame account password", "Open this link to choose a new website-account password:"
	case "change-email":
		return "Confirm your new Sesame account email", "Open this link to move your Sesame website account to this email address:"
	default:
		return "", ""
	}
}
