package config

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"strings"
)

// loadDotEnv applies KEY=value lines from path into the process environment,
// skipping any key that is already set. A missing file is not an error — in
// Docker the values arrive through env_file instead.
//
// Deliberately minimal: the same file is `include`d by the Makefile, so it has
// to stay within what GNU make can parse anyway (no quoting, no interpolation,
// no multi-line values).
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, strings.TrimSpace(value)); err != nil {
			return err
		}
	}
	return scanner.Err()
}
