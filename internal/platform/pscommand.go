package platform

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
)

// psQuote makes s a PowerShell string literal. PowerShell also takes the
// curly quotes ‘ ’ ‚ ‛ as quote marks, so those are doubled too.
func psQuote(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		switch r {
		case '\'', '\u2018', '\u2019', '\u201a', '\u201b':
			b.WriteRune(r)
		}
		b.WriteRune(r)
	}
	b.WriteByte('\'')
	return b.String()
}

// psEncode gives a script in the form PowerShell's -EncodedCommand takes
// (base64 of its UTF-16LE text). Scripts that need admin rights go this
// way, in the command line, rather than as a file someone else could change
// before it runs.
func psEncode(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// psAdminArgs is the PowerShell command line for a script that runs as
// admin. Each character of the script takes up about 2.7 characters here.
func psAdminArgs(script string) string {
	return "-NoProfile -NonInteractive -WindowStyle Hidden -EncodedCommand " + psEncode(script)
}

// shellExecuteMax is the longest command line Windows takes when it asks
// for admin permission (ShellExecute's limit, much shorter than the usual
// 32,767 characters), counting the program's path.
const shellExecuteMax = 2048

// psAdminCommand gives the arguments that run script in the elevated
// PowerShell exe. A script whose command line would be too long for
// Windows, as with long folder paths, goes in a file that write saves (it
// returns the file's path) and a short command runs it. That command reads
// the file once and runs it only if it's exactly what the app wrote (its
// SHA-256), so another program can't change it while Windows asks for
// permission.
func psAdminCommand(exe, script string, write func([]byte) (string, error)) (string, error) {
	fits := func(args string) bool { return len(`"`+exe+`" `)+len(args) < shellExecuteMax }
	if args := psAdminArgs(script); fits(args) {
		return args, nil
	}
	b := []byte(script)
	path, err := write(b)
	if err != nil {
		return "", fmt.Errorf("couldn’t save the script that needs admin rights: %w", err)
	}
	boot := fmt.Sprintf("$b=[IO.File]::ReadAllBytes(%s);"+
		"if((-join([Security.Cryptography.SHA256]::Create().ComputeHash($b)|%%{$_.ToString('x2')}))-ne'%x'){exit 1};"+
		"& ([scriptblock]::Create([Text.Encoding]::UTF8.GetString($b)))", psQuote(path), sha256.Sum256(b))
	args := psAdminArgs(boot)
	if !fits(args) {
		return "", errors.New("the folder paths are too long for Windows to ask for admin permission")
	}
	return args, nil
}
