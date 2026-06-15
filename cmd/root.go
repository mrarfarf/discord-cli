// Package cmd defines the command-line interface commands
package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/chrischapin/discord-cli/internal/config"
	"github.com/chrischapin/discord-cli/internal/consts"
	"github.com/chrischapin/discord-cli/internal/keyring"
	"github.com/chrischapin/discord-cli/internal/logger"
	"github.com/diamondburned/arikawa/v3/discord"
	"github.com/diamondburned/arikawa/v3/utils/ws"
	"github.com/diamondburned/ningen/v3"
	"github.com/spf13/cobra"
)

var (
	discordState *ningen.State
)

var (
	token      string
	configPath string
	logPath    string
	logLevel   string
	channelID  string
	filter     string
	hours      int

	rootCmd = &cobra.Command{
		Use:   consts.Name,
		Short: "Discord CLI tool for filtering channel messages",
		Long:  "A minimal CLI tool that listens to a Discord channel and outputs filtered messages to stdout.\n\nIf run with no flags, it will show a QR code for authentication.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// If no channel specified, show QR code for login
			if channelID == "" {
				fmt.Println("No channel specified. Starting QR code authentication...")
				fmt.Println("This will save your token for future use.")

				token, err := loginWithQRCLI()
				if err != nil {
					return fmt.Errorf("QR login failed: %w", err)
				}

				if err := keyring.SetToken(token); err != nil {
					slog.Warn("Failed to save token to keyring", "err", err)
					fmt.Println("Warning: Failed to save token to keyring, but login was successful.")
				} else {
					fmt.Println("\n✓ Token saved successfully!")
					fmt.Println("You can now run with --channel flag to listen to messages.")
				}
				return nil
			}

			var level slog.Level
			switch logLevel {
			case "debug":
				ws.EnableRawEvents = true
				level = slog.LevelDebug
			case "info":
				level = slog.LevelInfo
			case "warn":
				level = slog.LevelWarn
			case "error":
				level = slog.LevelError
			default:
				return fmt.Errorf("invalid log level %q (valid: debug, info, warn, error)", logLevel)
			}

			if err := logger.Load(logPath, level); err != nil {
				return fmt.Errorf("failed to load logger: %w", err)
			}

			// Load config (optional, only used for status)
			cfg, err := config.Load(configPath)
			if err != nil {
				// Config is optional, use defaults (invisible: quiet listener)
				cfg = &config.Config{Status: discord.InvisibleStatus}
			}

			if token == "" {
				token = os.Getenv("DISCORD_CLI_TOKEN")
			}

			if token == "" {
				token, err = keyring.GetToken()
				if err != nil {
					slog.Info("failed to retrieve token from keyring", "err", err)
					return fmt.Errorf("no token found. Run without --channel flag to authenticate with QR code, or provide --token flag")
				}
			}

			// Parse channel ID
			chID, err := discord.ParseSnowflake(channelID)
			if err != nil {
				return fmt.Errorf("invalid channel ID: %w", err)
			}

			// Parse filter words
			var filterWords []string
			if filter != "" {
				filterWords = strings.Split(filter, ",")
				for i := range filterWords {
					filterWords[i] = strings.TrimSpace(strings.ToLower(filterWords[i]))
				}
			}

			return runCLI(token, discord.ChannelID(chID), filterWords, cfg, hours)
		},
	}

	Execute = rootCmd.Execute
)

func init() {
	flags := rootCmd.Flags()
	flags.StringVar(&token, "token", "", "authentication token (default: $DISCORD_CLI_TOKEN or keyring)")
	flags.StringVar(&configPath, "config-path", config.DefaultPath(), "path of the configuration file")
	flags.StringVar(&logPath, "log-path", logger.DefaultPath(), "path of the log file")
	flags.StringVar(&logLevel, "log-level", "info", "log level")
	flags.StringVar(&channelID, "channel", "", "Discord channel ID to listen to (required)")
	flags.StringVar(&filter, "filter", "", "comma-separated words to filter messages (e.g., 'buy,sell,TSLA')")
	flags.IntVar(&hours, "hours", 0, "fetch and display messages from the past X hours (0 = only new messages)")
}
