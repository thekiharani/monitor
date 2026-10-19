package email

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/thekiharani/monitor/internal/cloud/config"
)

type Sender interface {
	Send(context.Context, Message) error
}

type Message struct {
	To, Subject, Text, HTML string
}
type SES struct {
	client *sesv2.Client
	from   string
}

func New(ctx context.Context, c config.Config) (Sender, error) {
	Configure(c.AppOrigin)
	switch strings.ToLower(c.EmailProvider) {
	case "mailpit":
		if c.Env == "production" {
			return nil, fmt.Errorf("mailpit is not allowed in production")
		}
		if c.SMTPHost == "" || c.SMTPFromEmail == "" {
			return unavailable{}, nil
		}
		return &SMTP{host: c.SMTPHost, port: c.SMTPPort, from: c.SMTPFromEmail}, nil
	case "ses":
	default:
		return nil, fmt.Errorf("unsupported EMAIL_PROVIDER %q", c.EmailProvider)
	}
	if c.AWSRegion == "" || c.SESFromEmail == "" {
		return unavailable{}, nil
	}
	ac, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(c.AWSRegion))
	if err != nil {
		return nil, err
	}
	return &SES{sesv2.NewFromConfig(ac), c.SESFromEmail}, nil
}

type SMTP struct {
	host string
	port int
	from string
}

func (s *SMTP) Send(parent context.Context, message Message) error {
	from, err := safeAddress(s.from)
	if err != nil {
		return err
	}
	to, err := safeAddress(message.To)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(s.host, fmt.Sprint(s.port)))
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	client, err := smtp.NewClient(conn, s.host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer client.Close()
	if err = client.Mail(from); err != nil {
		return err
	}
	if err = client.Rcpt(to); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	body, err := mimeBody(message)
	if err != nil {
		return err
	}
	subject := mime.QEncoding.Encode("UTF-8", strings.NewReplacer("\r", "", "\n", "").Replace(message.Subject))
	header := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\n", from, to, subject)
	if _, err = io.WriteString(writer, header+body); err != nil {
		_ = writer.Close()
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func safeAddress(value string) (string, error) {
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("email address contains a newline")
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value {
		return "", fmt.Errorf("invalid email address")
	}
	return address.Address, nil
}

func (s *SES) Send(parent context.Context, message Message) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	body := &types.Body{Text: &types.Content{Data: aws.String(message.Text), Charset: aws.String("UTF-8")}}
	if message.HTML != "" {
		body.Html = &types.Content{Data: aws.String(message.HTML), Charset: aws.String("UTF-8")}
	}
	_, err := s.client.SendEmail(ctx, &sesv2.SendEmailInput{FromEmailAddress: aws.String(s.from), Destination: &types.Destination{ToAddresses: []string{message.To}}, Content: &types.EmailContent{Simple: &types.Message{Subject: &types.Content{Data: aws.String(message.Subject), Charset: aws.String("UTF-8")}, Body: body}}})
	return err
}

func mimeBody(message Message) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.SetBoundary(fmt.Sprintf("monitor-%d", time.Now().UnixNano())); err != nil {
		return "", err
	}
	textPart, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=UTF-8"}, "Content-Transfer-Encoding": {"8bit"}})
	if err != nil {
		return "", err
	}
	if _, err = io.WriteString(textPart, strings.ReplaceAll(message.Text, "\r", "")); err != nil {
		return "", err
	}
	if message.HTML != "" {
		htmlPart, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/html; charset=UTF-8"}, "Content-Transfer-Encoding": {"8bit"}})
		if err != nil {
			return "", err
		}
		if _, err = io.WriteString(htmlPart, strings.ReplaceAll(message.HTML, "\r", "")); err != nil {
			return "", err
		}
	}
	if err = mw.Close(); err != nil {
		return "", err
	}
	return fmt.Sprintf("Content-Type: multipart/alternative; boundary=%q\r\n\r\n%s", mw.Boundary(), body.String()), nil
}

type unavailable struct{}

func (unavailable) Send(context.Context, Message) error {
	return fmt.Errorf("email delivery unavailable")
}
