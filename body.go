package main

import (
	"io"
	"os"
	"strings"
)

// stdin is the source for "-" file arguments (replaceable in tests).
var stdin io.Reader = os.Stdin

// readInput reads a file argument, where "-" means stdin.
func readInput(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(stdin)
	}
	return os.ReadFile(path)
}

// resolveBody returns the text given either inline or via --body-file
// (path or "-" for stdin). Exactly one source must be used and the result
// must not be blank. Trailing whitespace from files is dropped; leading
// indentation is preserved for Markdown.
func resolveBody(inline, file, what string) (string, error) {
	if file != "" {
		if inline != "" {
			return "", usageErrorf("pass the %s either inline or via --body-file, not both", what)
		}
		b, err := readInput(file)
		if err != nil {
			return "", err
		}
		inline = strings.TrimRight(string(b), " \t\r\n")
	}
	if strings.TrimSpace(inline) == "" {
		return "", usageErrorf("missing %s (pass it as an argument or via --body-file)", what)
	}
	return inline, nil
}
