package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/chrischapin/discord-cli/internal/config"
	"github.com/chrischapin/discord-cli/internal/http"
	"github.com/chrischapin/discord-cli/internal/keyring"
	"github.com/diamondburned/arikawa/v3/api"
	"github.com/diamondburned/arikawa/v3/discord"
	"github.com/diamondburned/arikawa/v3/gateway"
	"github.com/diamondburned/arikawa/v3/session"
	"github.com/diamondburned/arikawa/v3/state"
	"github.com/diamondburned/arikawa/v3/state/store/defaultstore"
	"github.com/diamondburned/arikawa/v3/utils/handler"
	"github.com/diamondburned/arikawa/v3/utils/httputil"
	"github.com/diamondburned/arikawa/v3/utils/httputil/httpdriver"
	"github.com/diamondburned/arikawa/v3/utils/ws"
	"github.com/diamondburned/ningen/v3"
)

// maxSeenMessages bounds the dedup set so a listener running for days doesn't
// grow memory without limit. Evicted IDs are the oldest ones, which by then are
// far outside any realistic redelivery/overlap window.
const maxSeenMessages = 10000

var (
	targetChannelID discord.ChannelID
	filterWords     []string
	lookbackHours   int
	quitChan        = make(chan struct{})
	messageCounter  uint64
	counterMutex    sync.Mutex
	historicalDone  = make(chan struct{})
	historicalOnce  sync.Once

	// Bounded FIFO set of recently seen message IDs.
	seenMutex    sync.Mutex
	seenMessages = make(map[discord.MessageID]struct{}, maxSeenMessages)
	seenRing     = make([]discord.MessageID, maxSeenMessages)
	seenIdx      int
	seenCount    int
)

// alreadySeen reports whether id is in the dedup set without recording it.
func alreadySeen(id discord.MessageID) bool {
	seenMutex.Lock()
	_, ok := seenMessages[id]
	seenMutex.Unlock()
	return ok
}

// markSeen records id and reports whether it was already present, atomically.
// When the set is full the oldest entry is evicted (FIFO).
func markSeen(id discord.MessageID) (already bool) {
	seenMutex.Lock()
	defer seenMutex.Unlock()
	if _, ok := seenMessages[id]; ok {
		return true
	}
	if seenCount == maxSeenMessages {
		delete(seenMessages, seenRing[seenIdx])
	} else {
		seenCount++
	}
	seenRing[seenIdx] = id
	seenIdx = (seenIdx + 1) % maxSeenMessages
	seenMessages[id] = struct{}{}
	return false
}

// ANSI color codes for terminal output
var colors = []string{
	"\033[31m", // Red
	"\033[32m", // Green
	"\033[33m", // Yellow
	"\033[34m", // Blue
	"\033[35m", // Magenta
	"\033[36m", // Cyan
	"\033[91m", // Bright Red
	"\033[92m", // Bright Green
	"\033[93m", // Bright Yellow
	"\033[94m", // Bright Blue
	"\033[95m", // Bright Magenta
	"\033[96m", // Bright Cyan
}

const resetColor = "\033[0m"

func runCLI(token string, channelID discord.ChannelID, filters []string, cfg *config.Config, hours int) error {
	targetChannelID = channelID
	filterWords = filters
	lookbackHours = hours

	// Print instance ID for tracking (only if not in historical mode)
	instanceID := cfg.InstanceID
	if instanceID == "" {
		instanceID = "unknown"
	}
	if hours == 0 {
		fmt.Printf("Instance ID: %s\n", instanceID)
	}
	slog.Info("Starting Discord client", "instance_id", instanceID, "channel_id", channelID)

	identifyProps := http.IdentifyProperties(instanceID)

	api.UserAgent = http.BrowserUserAgent
	gateway.DefaultIdentity = identifyProps

	// Guard: never announce an online presence by accident. An empty status is
	// rendered as "online" by Discord, so coerce it to invisible here.
	presenceStatus := cfg.Status
	if presenceStatus == "" {
		presenceStatus = discord.InvisibleStatus
	}
	gateway.DefaultPresence = &gateway.UpdatePresenceCommand{
		Status: presenceStatus,
	}

	id := gateway.DefaultIdentifier(token)
	id.Compress = false

	session := session.NewCustom(id, http.NewClient(token), handler.New())
	state := state.NewFromSession(session, defaultstore.New())
	discordState = ningen.FromState(state)

	// Handlers
	discordState.AddHandler(onRaw)
	discordState.AddHandler(onReady)
	discordState.AddHandler(onMessageCreate)

	discordState.StateLog = func(err error) {
		slog.Error("state log", "err", err)
	}

	discordState.OnRequest = append(discordState.OnRequest, httputil.WithHeaders(http.Headers(instanceID)), onRequest)

	slog.Info("Connecting to Discord...", "channel_id", channelID)
	if err := discordState.Open(context.TODO()); err != nil {
		// Close the failed state before retrying
		if discordState != nil {
			discordState.Close()
		}

		// Check if it's an authentication error (REST 401 or gateway 4004).
		if isAuthError(err) {
			slog.Error("Token appears to be invalid or expired", "err", err)
			fmt.Println("\n⚠ Token is invalid or expired.")
			fmt.Println("Starting QR code authentication to get a new token...")

			newToken, qrErr := loginWithQRCLI()
			if qrErr != nil {
				return fmt.Errorf("QR login failed: %w", qrErr)
			}

			// Save new token
			if err := keyring.SetToken(newToken); err != nil {
				slog.Warn("Failed to save token to keyring", "err", err)
			} else {
				fmt.Println("\n✓ New token saved successfully!")
			}

			// Retry connection with new token
			slog.Info("Retrying connection with new token...")
			return runCLI(newToken, channelID, filters, cfg, lookbackHours)
		}
		return fmt.Errorf("failed to open Discord connection: %w", err)
	}

	// Handle graceful shutdown (Ctrl+C and 'Q' key)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// If hours is specified, wait for historical fetch to complete, then exit
	if hours > 0 {
		select {
		case <-historicalDone:
			slog.Info("Historical messages fetched, exiting...")
		case <-sigChan:
			slog.Info("Shutting down (Ctrl+C)...")
		}
	} else {
		// Start keyboard input handler for 'Q' key (only in streaming mode)
		go handleKeyboardInput()

		select {
		case <-sigChan:
			slog.Info("Shutting down (Ctrl+C)...")
		case <-quitChan:
			slog.Info("Shutting down (Q pressed)...")
		}
	}

	if err := discordState.Close(); err != nil {
		return fmt.Errorf("failed to close Discord connection: %w", err)
	}

	return nil
}

// isAuthError reports whether err represents a Discord authentication failure:
// a REST 401 (httputil.HTTPError) or a gateway "authentication failed" close
// (ws.CloseEvent with code 4004). This is more reliable than matching on the
// error string.
func isAuthError(err error) bool {
	var httpErr *httputil.HTTPError
	if errors.As(err, &httpErr) && httpErr.Status == 401 {
		return true
	}
	var closeErr *ws.CloseEvent
	if errors.As(err, &closeErr) && closeErr.Code == 4004 {
		return true
	}
	return false
}

func handleKeyboardInput() {
	// Only handle keyboard input if stdin is a terminal
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return
	}

	// Use line-based input (user presses Q then Enter)
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if strings.TrimSpace(strings.ToLower(line)) == "q" {
			close(quitChan)
			return
		}
	}
}

func onRequest(r httpdriver.Request) error {
	if req, ok := r.(*httpdriver.DefaultRequest); ok {
		slog.Debug("new HTTP request", "method", req.Method, "url", req.URL)
	}
	return nil
}

func onRaw(event *ws.RawEvent) {
	slog.Debug(
		"new raw event",
		"code", event.OriginalCode,
		"type", event.OriginalType,
	)
}

func onReady(r *gateway.ReadyEvent) {
	slog.Info("Connected to Discord", "user", r.User.Username)
	slog.Info("Listening for messages in channel", "channel_id", targetChannelID)

	// Verify channel exists and we have access
	channel, err := discordState.Cabinet.Channel(targetChannelID)
	if err != nil {
		slog.Error("Failed to access channel", "channel_id", targetChannelID, "err", err)
		slog.Error("Please verify the channel ID is correct and you have access to it")
		// Close connection and exit gracefully
		go func() {
			if err := discordState.Close(); err != nil {
				slog.Error("Failed to close connection", "err", err)
			}
			os.Exit(1)
		}()
		return
	}

	slog.Info("Channel verified", "channel_name", channel.Name)
	if len(filterWords) > 0 {
		slog.Info("Filter active", "words", filterWords)
	} else {
		slog.Info("No filter specified, all messages will be output")
	}

	// Only show interactive messages if not in historical mode
	if lookbackHours == 0 {
		fmt.Println("\nPress 'Q' + Enter to quit (or Ctrl+C)")
	}

	// Fetch historical messages if requested.
	// onReady fires again on every gateway resume/reconnect, so guard the
	// one-shot fetch + channel close to avoid re-fetching and a double-close panic.
	if lookbackHours > 0 {
		historicalOnce.Do(func() {
			fetchHistoricalMessages(targetChannelID, filterWords, lookbackHours)
			close(historicalDone)
		})
	}
}

func fetchHistoricalMessages(channelID discord.ChannelID, filters []string, hours int) {
	slog.Info("Fetching historical messages", "hours", hours)

	// Calculate the timestamp X hours ago
	cutoffTime := time.Now().Add(-time.Duration(hours) * time.Hour)

	// Fetch messages (Discord API allows up to 100 messages per request)
	// We'll need to paginate if there are more than 100 messages
	var allMessages []discord.Message
	var beforeID discord.MessageID

	for {
		// Fetch up to 100 messages before the last message ID (or from the end)
		var messages []discord.Message
		var err error

		if beforeID.IsValid() {
			messages, err = discordState.Client.MessagesBefore(channelID, beforeID, 100)
		} else {
			messages, err = discordState.Client.Messages(channelID, 100)
		}

		if err != nil {
			slog.Error("Failed to fetch messages", "err", err)
			break
		}

		if len(messages) == 0 {
			break
		}

		// Collect messages newer than the cutoff; stop once we cross it.
		reachedCutoff := false
		for _, msg := range messages {
			if msg.Timestamp.Time().Before(cutoffTime) {
				reachedCutoff = true
				continue
			}
			allMessages = append(allMessages, msg)
		}

		// Done if we've crossed the cutoff or exhausted the channel history.
		if reachedCutoff || len(messages) < 100 {
			break
		}

		// Page back from the oldest message in this batch.
		beforeID = messages[len(messages)-1].ID
	}

	// Process messages in chronological order (oldest first)
	for i := len(allMessages) - 1; i >= 0; i-- {
		msg := allMessages[i]
		if msg.Timestamp.Time().Before(cutoffTime) {
			continue
		}
		outputMessage(msg, filters)
	}

	slog.Info("Finished fetching historical messages", "count", len(allMessages))

	// Signal completion (channel will be closed in onReady)
}

// formatEmbedAsText returns a readable text representation of an embed.
func formatEmbedAsText(e discord.Embed) string {
	var parts []string
	if e.Author != nil && e.Author.Name != "" {
		parts = append(parts, "[Embed Author: "+e.Author.Name+"]")
	}
	if e.Title != "" {
		parts = append(parts, e.Title)
	}
	if e.Description != "" {
		parts = append(parts, e.Description)
	}
	for _, f := range e.Fields {
		if f.Name != "" || f.Value != "" {
			parts = append(parts, f.Name+": "+f.Value)
		}
	}
	if e.Footer != nil && e.Footer.Text != "" {
		parts = append(parts, "[Footer: "+e.Footer.Text+"]")
	}
	return strings.Join(parts, " | ")
}

// formatMessageContent returns all text content from a message (content + embeds) for filtering.
func formatMessageContent(content string, embeds []discord.Embed) string {
	parts := []string{content}
	for _, e := range embeds {
		if s := formatEmbedAsText(e); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

// outputEmbed prints embed content with the given prefix.
func outputEmbed(color, timestamp, author, prefix string, e discord.Embed) {
	text := formatEmbedAsText(e)
	if text == "" {
		return
	}
	fmt.Printf("%s[%s]%s %s: %s%s\n", color, timestamp, resetColor, author, prefix, text)
}

func outputMessage(message discord.Message, filters []string) {
	// Cheap early-out for already-seen messages (avoids building search content).
	if alreadySeen(message.ID) {
		return
	}

	// Build searchable content for filtering (main content + embeds + referenced/forwarded)
	searchContent := formatMessageContent(message.Content, message.Embeds)
	if message.ReferencedMessage != nil {
		ref := message.ReferencedMessage
		searchContent += " " + formatMessageContent(ref.Content, ref.Embeds)
	}
	for _, snap := range message.MessageSnapshots {
		snapMsg := snap.Message
		searchContent += " " + formatMessageContent(snapMsg.Content, snapMsg.Embeds)
	}

	// Filter messages
	if len(filters) > 0 {
		searchLower := strings.ToLower(searchContent)
		matched := false
		for _, word := range filters {
			if strings.Contains(searchLower, word) {
				matched = true
				break
			}
		}
		if !matched {
			return
		}
	}

	// Mark as seen before outputting; if another goroutine beat us to this ID
	// (e.g. historical fetch overlapping a live event), skip the duplicate.
	if markSeen(message.ID) {
		return
	}

	// Get a color for this message (cycle through colors)
	counterMutex.Lock()
	colorIndex := messageCounter % uint64(len(colors))
	messageCounter++
	counterMutex.Unlock()
	color := colors[colorIndex]

	// Output message to stdout with colored timestamp
	timestamp := message.Timestamp.Time().In(time.Local).Format(time.RFC3339)
	author := message.Author.DisplayOrUsername()

	// Main message content (only print if non-empty, or if we have nothing else)
	if message.Content != "" {
		fmt.Printf("%s[%s]%s %s: %s\n", color, timestamp, resetColor, author, message.Content)
	}

	// Output embeds from the main message
	for _, e := range message.Embeds {
		outputEmbed(color, timestamp, author, "[Embed] ", e)
	}

	// Output referenced message (reply)
	if message.ReferencedMessage != nil {
		ref := message.ReferencedMessage
		refAuthor := ref.Author.DisplayOrUsername()
		refPrefix := "  └─ [Replied to " + refAuthor + "] "
		if ref.Content != "" {
			fmt.Printf("%s[%s]%s %s: %s%s\n", color, timestamp, resetColor, author, refPrefix, ref.Content)
		}
		for _, e := range ref.Embeds {
			outputEmbed(color, timestamp, author, refPrefix+"[Embed] ", e)
		}
		for _, att := range ref.Attachments {
			fmt.Printf("%s[%s]%s %s: %s[Attachment] %s\n", color, timestamp, resetColor, author, refPrefix, att.URL)
		}
	}

	// Output forwarded message snapshots
	for _, snap := range message.MessageSnapshots {
		snapMsg := snap.Message
		// MessageSnapshot doesn't have Author; use "Forwarded" as label
		snapLabel := "  └─ [Forwarded] "
		if snapMsg.Content != "" {
			fmt.Printf("%s[%s]%s %s: %s%s\n", color, timestamp, resetColor, author, snapLabel, snapMsg.Content)
		}
		for _, e := range snapMsg.Embeds {
			outputEmbed(color, timestamp, author, snapLabel+"[Embed] ", e)
		}
		for _, att := range snapMsg.Attachments {
			fmt.Printf("%s[%s]%s %s: %s[Attachment] %s\n", color, timestamp, resetColor, author, snapLabel, att.URL)
		}
	}

	// Output attachments if any
	for _, att := range message.Attachments {
		fmt.Printf("%s[%s]%s %s: [Attachment] %s\n", color, timestamp, resetColor, author, att.URL)
	}

	// Add blank line after each message for easier parsing
	fmt.Println()
}

func onMessageCreate(message *gateway.MessageCreateEvent) {
	// Skip new messages if we're in historical-only mode
	if lookbackHours > 0 {
		return
	}

	// Only process messages from the target channel
	if message.ChannelID != targetChannelID {
		return
	}

	// Skip bot messages if desired (you can remove this if you want bot messages)
	// if message.Author.Bot {
	// 	return
	// }

	// Use the shared output function
	outputMessage(message.Message, filterWords)
}
