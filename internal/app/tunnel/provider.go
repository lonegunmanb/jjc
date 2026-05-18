package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	Cloudflared = "cloudflared"
	None        = "none"
)

const cloudflaredDownloadURL = "https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/downloads/"

var quickTunnelURLRE = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

type Provider interface {
	Start(ctx context.Context, localAddr string) (string, error)
	Stop() error
	Name() string
}

type CloudflaredProvider struct {
	binary     string
	httpClient *http.Client
	logger     *log.Logger

	mu      sync.Mutex
	cmd     *exec.Cmd
	waitErr chan error
	stopped bool
}

type CloudflaredOption func(*CloudflaredProvider)

func WithBinary(path string) CloudflaredOption {
	return func(p *CloudflaredProvider) {
		p.binary = path
	}
}

func WithHTTPClient(client *http.Client) CloudflaredOption {
	return func(p *CloudflaredProvider) {
		if client != nil {
			p.httpClient = client
		}
	}
}

func WithLogger(logger *log.Logger) CloudflaredOption {
	return func(p *CloudflaredProvider) {
		if logger != nil {
			p.logger = logger
		}
	}
}

func NewCloudflaredProvider(opts ...CloudflaredOption) (*CloudflaredProvider, error) {
	p := &CloudflaredProvider{
		binary:     Cloudflared,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		logger:     log.Default(),
	}
	for _, opt := range opts {
		opt(p)
	}
	path, err := exec.LookPath(p.binary)
	if err != nil {
		return nil, fmt.Errorf("cloudflared binary not found; install it from %s or run with --tunnel=none to manage the callback URL manually: %w", cloudflaredDownloadURL, err)
	}
	p.binary = path
	return p, nil
}

func (p *CloudflaredProvider) Name() string { return Cloudflared }

func (p *CloudflaredProvider) Start(ctx context.Context, localAddr string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	target, err := localHTTPURL(localAddr)
	if err != nil {
		return "", err
	}
	cmd := exec.Command(p.binary, "tunnel", "--url", target)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("cloudflared stderr pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("cloudflared stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start cloudflared: %w", err)
	}

	p.mu.Lock()
	p.cmd = cmd
	p.waitErr = make(chan error, 1)
	p.stopped = false
	p.mu.Unlock()

	go func() { p.waitErr <- cmd.Wait() }()

	urlCh := make(chan string, 1)
	logCh := make(chan string, 16)
	go scanForURL(stderr, urlCh, logCh)
	go scanForURL(stdout, urlCh, logCh)
	go func() {
		for line := range logCh {
			p.logger.Printf("event=cloudflared_output line=%q", line)
		}
	}()

	var publicURL string
	select {
	case publicURL = <-urlCh:
	case err := <-p.waitErr:
		return "", fmt.Errorf("cloudflared exited before printing quick tunnel URL: %w", err)
	case <-ctx.Done():
		_ = p.Stop()
		return "", fmt.Errorf("cloudflared quick tunnel startup canceled: %w", ctx.Err())
	}
	publicURL = NormalizePublicURL(publicURL)
	if err := p.waitForHEAD(ctx, publicURL); err != nil {
		_ = p.Stop()
		return "", err
	}
	return publicURL, nil
}

func (p *CloudflaredProvider) Stop() error {
	p.mu.Lock()
	cmd := p.cmd
	waitErr := p.waitErr
	if cmd == nil || cmd.Process == nil || p.stopped {
		p.mu.Unlock()
		return nil
	}
	p.stopped = true
	p.mu.Unlock()

	if err := terminateProcess(cmd.Process); err != nil && !errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("stop cloudflared: %w", err)
	}
	if waitErr == nil {
		return nil
	}
	select {
	case <-waitErr:
		return nil
	case <-time.After(5 * time.Second):
		if err := cmd.Process.Kill(); err != nil {
			return fmt.Errorf("kill cloudflared: %w", err)
		}
		<-waitErr
		return nil
	}
}

func ParseQuickTunnelURL(line string) (string, bool) {
	match := quickTunnelURLRE.FindString(line)
	if match == "" {
		return "", false
	}
	return NormalizePublicURL(match), true
}

func NormalizePublicURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/") + "/"
}

func scanForURL(r io.Reader, urlCh chan<- string, logCh chan<- string) {
	buf := make([]byte, 32*1024)
	var pending strings.Builder
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			pending.WriteString(chunk)
			for {
				line, rest, ok := strings.Cut(pending.String(), "\n")
				if !ok {
					break
				}
				pending.Reset()
				pending.WriteString(rest)
				logLine(line, urlCh, logCh)
			}
		}
		if err != nil {
			if pending.Len() > 0 {
				logLine(pending.String(), urlCh, logCh)
			}
			return
		}
	}
}

func logLine(line string, urlCh chan<- string, logCh chan<- string) {
	select {
	case logCh <- strings.TrimSpace(line):
	default:
	}
	if publicURL, ok := ParseQuickTunnelURL(line); ok {
		select {
		case urlCh <- publicURL:
		default:
		}
	}
}

func (p *CloudflaredProvider) waitForHEAD(ctx context.Context, publicURL string) error {
	deadline := time.Now().Add(30 * time.Second)
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, publicURL, nil)
		if err != nil {
			return fmt.Errorf("build cloudflared HEAD self-test request: %w", err)
		}
		resp, err := p.httpClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				p.logger.Printf("event=cloudflared_self_test_ok url=%s attempts=%d", publicURL, attempt)
				return nil
			}
			err = fmt.Errorf("unexpected status %d", resp.StatusCode)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cloudflared HEAD self-test failed for %s: %w", publicURL, err)
		}
		p.logger.Printf("event=cloudflared_self_test_retry url=%s attempt=%d err=%v", publicURL, attempt, err)
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return fmt.Errorf("cloudflared HEAD self-test canceled: %w", ctx.Err())
		}
	}
}

func localHTTPURL(addr string) (string, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("listen address %q must include a port for cloudflared: %w", addr, err)
	}
	if port == "0" || port == "" {
		return "", fmt.Errorf("listen address %q must resolve to a concrete port before cloudflared starts", addr)
	}
	return "http://localhost:" + port, nil
}
