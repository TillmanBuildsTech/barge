package transport

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/TillmanBuildsTech/barge/internal/domain"
)

var (
	safeWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)
	envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// shellQuote quotes s as exactly one POSIX-shell word. Everything outside a
// conservative safe set is wrapped in single quotes, with embedded single
// quotes written as '\”. Inside single quotes the shell interprets nothing.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if safeWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// buildRemote turns a Cmd into what is sent over SSH.
//
// SSH "exec" carries a single string that the remote login shell parses, so
// every argument is quoted and the result is wrapped in `sh -c` (so it works
// whatever the user's login shell is). Environment values are NOT placed in
// that string — it is visible in the remote process list. Instead they are
// sent as a shell preamble at the head of stdin, which the script reads with
// dd (exactly len(preamble) bytes, leaving the rest of stdin for the command).
func buildRemote(c Cmd) (command, preamble string, err error) {
	if len(c.Argv) == 0 || c.Argv[0] == "" {
		return "", "", fmt.Errorf("%w: empty command", domain.ErrInvalid)
	}
	words := make([]string, len(c.Argv))
	for i, a := range c.Argv {
		if strings.ContainsRune(a, 0) {
			return "", "", fmt.Errorf("%w: NUL byte in argument %d", domain.ErrInvalid, i)
		}
		words[i] = shellQuote(a)
	}
	script := "exec " + strings.Join(words, " ")

	if len(c.Env) > 0 {
		var b strings.Builder
		for _, kv := range c.Env {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || !envKeyRe.MatchString(k) {
				return "", "", fmt.Errorf("%w: invalid environment entry (want NAME=value with NAME matching %s)", domain.ErrInvalid, envKeyRe)
			}
			if strings.ContainsRune(v, 0) {
				return "", "", fmt.Errorf("%w: NUL byte in value of %s", domain.ErrInvalid, k)
			}
			b.WriteString("export " + k + "=" + shellQuote(v) + "\n")
		}
		preamble = b.String()
		script = `eval "$(dd bs=1 count=` + strconv.Itoa(len(preamble)) + ` 2>/dev/null)" && ` + script
	}
	return "sh -c " + shellQuote(script), preamble, nil
}
