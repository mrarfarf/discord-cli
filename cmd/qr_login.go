package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	apphttp "github.com/chrischapin/discord-cli/internal/http"
	"github.com/diamondburned/arikawa/v3/api"
	"github.com/gorilla/websocket"
	"github.com/skip2/go-qrcode"
)

const gatewayURL = "wss://remote-auth-gateway.discord.gg/?v=2"

var endpointRemoteAuthLogin = api.EndpointMe + "/remote-auth/login"

type cliQRLogin struct {
	conn        *websocket.Conn
	privKey     *rsa.PrivateKey
	cancel      context.CancelFunc
	fingerprint string
}

func renderQRCLI(content string) (string, error) {
	code, err := qrcode.New(content, qrcode.Low)
	if err != nil {
		return "", err
	}
	bm := code.Bitmap()
	var b strings.Builder
	for y := 0; y < len(bm); y += 2 {
		for x := range bm[y] {
			top := bm[y][x]
			bottom := false
			if y+1 < len(bm) {
				bottom = bm[y+1][x]
			}
			if top && bottom {
				b.WriteString("█")
			} else if top && !bottom {
				b.WriteString("▀")
			} else if !top && bottom {
				b.WriteString("▄")
			} else {
				b.WriteString(" ")
			}
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func exchangeTicketCLI(ctx context.Context, ticket string, fingerprint string, priv *rsa.PrivateKey) (string, error) {
	if ticket == "" {
		return "", errors.New("empty ticket")
	}
	body := map[string]string{"ticket": ticket}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := stdhttp.NewRequestWithContext(ctx, stdhttp.MethodPost, endpointRemoteAuthLogin, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}

	req.Header = apphttp.Headers("") // Empty instance ID for QR login is fine
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", apphttp.BrowserUserAgent)
	if fingerprint != "" {
		req.Header.Set("X-Fingerprint", fingerprint)
		req.Header.Set("Referer", "https://discord.com/ra/"+fingerprint)
	}

	client := &stdhttp.Client{Transport: apphttp.NewTransport(), Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("remote-auth login failed: %s: %s", resp.Status, string(b))
	}

	decoder := json.NewDecoder(resp.Body)

	var out struct {
		EncryptedToken string `json:"encrypted_token"`
	}
	if err := decoder.Decode(&out); err != nil {
		return "", err
	}
	if out.EncryptedToken == "" {
		return "", fmt.Errorf("no encrypted_token in response")
	}
	enc, err := base64.StdEncoding.DecodeString(out.EncryptedToken)
	if err != nil {
		return "", err
	}
	pt, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, priv, enc, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

func loginWithQRCLI() (string, error) {
	fmt.Println("Preparing QR code...")
	fmt.Println("Press Ctrl+C to cancel")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		cancel()
	}()

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", fmt.Errorf("failed to generate key: %w", err)
	}

	pubDER, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if err != nil {
		return "", fmt.Errorf("failed to marshal public key: %w", err)
	}
	encodedPublicKey := base64.StdEncoding.EncodeToString(pubDER)

	headers := stdhttp.Header{}
	headers.Set("User-Agent", apphttp.BrowserUserAgent)
	headers.Set("Origin", "https://discord.com")

	fmt.Println("Connecting to Remote Auth Gateway...")

	dialer := websocket.Dialer{
		Proxy:             stdhttp.ProxyFromEnvironment,
		HandshakeTimeout:  15 * time.Second,
		EnableCompression: true,
	}

	conn, resp, err := dialer.DialContext(ctx, gatewayURL, headers)
	if err != nil {
		var body []byte
		if resp != nil && resp.Body != nil {
			body, _ = io.ReadAll(resp.Body)
		}
		status := ""
		if resp != nil {
			status = resp.Status
		}
		return "", fmt.Errorf("websocket dial failed: %w, status=%s, body=%s", err, status, string(body))
	}
	defer conn.Close()

	readCh := make(chan []byte, 1)
	readErr := make(chan error, 1)
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			readCh <- data
		}
	}()

	var heartbeatTicker *time.Ticker
	defer func() {
		if heartbeatTicker != nil {
			heartbeatTicker.Stop()
		}
	}()

	var fingerprint string

	for {
		select {
		case <-ctx.Done():
			return "", errors.New("login canceled")
		case err := <-readErr:
			if err != nil {
				return "", fmt.Errorf("websocket read error: %w", err)
			}
			return "", errors.New("websocket closed")
		case data := <-readCh:
			var opOnly struct {
				Op string `json:"op"`
			}
			if err := json.Unmarshal(data, &opOnly); err != nil {
				return "", fmt.Errorf("bad JSON: %w", err)
			}

			switch opOnly.Op {
			case "hello":
				var h struct {
					TimeoutMs         int `json:"timeout_ms"`
					HeartbeatInterval int `json:"heartbeat_interval"`
				}
				if err := json.Unmarshal(data, &h); err != nil {
					return "", fmt.Errorf("hello decode failed: %w", err)
				}
				if h.HeartbeatInterval > 0 {
					heartbeatTicker = time.NewTicker(time.Duration(h.HeartbeatInterval) * time.Millisecond)
					go func() {
						for {
							select {
							case <-ctx.Done():
								return
							case <-heartbeatTicker.C:
								conn.WriteJSON(map[string]any{"op": "heartbeat"})
							}
						}
					}()
				}
				fmt.Println("Connected. Handshaking...")
				if err := conn.WriteJSON(map[string]any{
					"op":                 "init",
					"encoded_public_key": encodedPublicKey,
				}); err != nil {
					return "", fmt.Errorf("init send failed: %w", err)
				}
			case "nonce_proof":
				var n struct {
					EncryptedNonce string `json:"encrypted_nonce"`
				}
				if err := json.Unmarshal(data, &n); err != nil {
					return "", fmt.Errorf("nonce decode failed: %w", err)
				}
				enc, err := base64.StdEncoding.DecodeString(n.EncryptedNonce)
				if err != nil {
					return "", fmt.Errorf("nonce b64 decode failed: %w", err)
				}
				pt, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, privKey, enc, nil)
				if err != nil {
					return "", fmt.Errorf("nonce decrypt failed: %w", err)
				}
				nonce := base64.RawURLEncoding.EncodeToString(pt)
				if err := conn.WriteJSON(map[string]any{"op": "nonce_proof", "nonce": nonce}); err != nil {
					return "", fmt.Errorf("nonce send failed: %w", err)
				}
			case "pending_remote_init":
				var p struct {
					Fingerprint string `json:"fingerprint"`
				}
				if err := json.Unmarshal(data, &p); err != nil {
					return "", fmt.Errorf("init decode failed: %w", err)
				}
				fingerprint = p.Fingerprint
				content := "https://discord.com/ra/" + p.Fingerprint
				ascii, err := renderQRCLI(content)
				if err != nil {
					return "", fmt.Errorf("QR render failed: %w", err)
				}
				fmt.Println("\n" + ascii)
				fmt.Println("\nScan with Discord mobile app")
				fmt.Println("Press Ctrl+C to cancel")
			case "heartbeat_ack":
				// No action needed
			case "pending_ticket":
				var t struct {
					EncryptedUserPayload string `json:"encrypted_user_payload"`
				}
				if err := json.Unmarshal(data, &t); err != nil {
					return "", fmt.Errorf("ticket decode failed: %w", err)
				}
				payload, err := base64.StdEncoding.DecodeString(t.EncryptedUserPayload)
				if err != nil {
					return "", fmt.Errorf("ticket payload b64 failed: %w", err)
				}
				pt, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, privKey, payload, nil)
				if err != nil {
					return "", fmt.Errorf("ticket payload decrypt failed: %w", err)
				}
				parts := strings.SplitN(string(pt), ":", 4)
				var discriminator, username string
				if len(parts) == 4 {
					discriminator = parts[1]
					username = parts[3]
				}
				if discriminator == "" && username == "" {
					fmt.Println("Scan received. Waiting for approval on mobile...")
				} else {
					fmt.Printf("Logging in as %s#%s\n", username, discriminator)
					fmt.Println("Confirm on mobile...")
				}
			case "pending_login":
				var p struct {
					Ticket string `json:"ticket"`
				}
				if err := json.Unmarshal(data, &p); err != nil {
					return "", fmt.Errorf("login decode failed: %w", err)
				}
				fmt.Println("Authenticating...")
				token, err := exchangeTicketCLI(ctx, p.Ticket, fingerprint, privKey)
				if err != nil {
					return "", fmt.Errorf("ticket exchange failed: %w", err)
				}
				return token, nil
			case "cancel":
				return "", errors.New("login canceled on mobile")
			default:
			}
		}
	}
}
