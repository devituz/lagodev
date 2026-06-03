package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/devituz/lagodev/crypt"
	"github.com/spf13/cobra"
)

// NewKeyGenerate returns `key:generate` — Laravel-style command that
// produces a base64-encoded 32-byte AES-256 key and either writes it
// to a .env file (under APP_KEY) or prints it for the caller to
// capture themselves.
//
// Flags:
//
//	--env <path>   path to .env (default ".env"); written when present
//	--show         print the key to stdout in addition to writing
//	--force        overwrite an existing APP_KEY value
//	--print-only   print to stdout and do NOT touch any file
func NewKeyGenerate(_ *Env) *cobra.Command {
	var (
		envPath   string
		show      bool
		force     bool
		printOnly bool
	)
	c := &cobra.Command{
		Use:   "key:generate",
		Short: "Generate a fresh APP_KEY (AES-256 / 32 bytes, base64-encoded)",
		Long: `Generates 32 random bytes (AES-256) and base64-encodes them under the
"base64:" prefix. Without --print-only, writes the value to APP_KEY in
the target .env file; refuses to overwrite an existing APP_KEY unless
--force is passed.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, err := crypt.GenerateKeyString()
			if err != nil {
				return fmt.Errorf("key:generate: %w", err)
			}
			if printOnly {
				fmt.Fprintln(cmd.OutOrStdout(), k)
				return nil
			}
			if err := writeAppKey(envPath, k, force); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "APP_KEY written to %s\n", envPath)
			if show {
				fmt.Fprintln(cmd.OutOrStdout(), k)
			}
			return nil
		},
	}
	c.Flags().StringVar(&envPath, "env", ".env", "path to the .env file to update")
	c.Flags().BoolVar(&show, "show", false, "also print the key to stdout")
	c.Flags().BoolVar(&force, "force", false, "overwrite an existing APP_KEY value")
	c.Flags().BoolVar(&printOnly, "print-only", false, "print the key only; do not touch any file")
	return c
}

// writeAppKey inserts or updates the APP_KEY line in path. The function
// preserves all other lines and comments. Path is created if missing.
func writeAppKey(path, value string, force bool) error {
	var lines []string
	existed := false
	if data, err := os.ReadFile(path); err == nil {
		existed = true
		s := bufio.NewScanner(strings.NewReader(string(data)))
		s.Buffer(make([]byte, 64*1024), 1024*1024)
		replaced := false
		for s.Scan() {
			line := s.Text()
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "APP_KEY=") || strings.HasPrefix(trim, "APP_KEY =") {
				if !force {
					existing := strings.TrimPrefix(strings.TrimPrefix(trim, "APP_KEY="), " ")
					existing = strings.TrimPrefix(existing, "= ")
					if existing != "" {
						return fmt.Errorf("APP_KEY already set in %s (use --force to overwrite)", path)
					}
				}
				lines = append(lines, "APP_KEY="+value)
				replaced = true
				continue
			}
			lines = append(lines, line)
		}
		if err := s.Err(); err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if !replaced {
			lines = append(lines, "APP_KEY="+value)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	} else {
		lines = []string{"APP_KEY=" + value}
	}
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	_ = existed // reserved for future logging
	return nil
}
