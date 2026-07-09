package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// prompter reads interactive input. It keeps a single buffered reader so
// successive line reads don't drop data buffered by an earlier read.
type prompter struct {
	in  io.Reader
	br  *bufio.Reader
	out io.Writer
}

func newPrompter(in io.Reader, out io.Writer) *prompter {
	return &prompter{in: in, br: bufio.NewReader(in), out: out}
}

// line writes label and reads a single line, trimming the trailing newline.
func (p *prompter) line(label string) (string, error) {
	fmt.Fprint(p.out, label)
	s, err := p.br.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}

// password writes label and reads a line without echoing when the input is a
// terminal; otherwise (pipe, test) it falls back to a plain line read.
func (p *prompter) password(label string) (string, error) {
	fmt.Fprint(p.out, label)
	if f, ok := p.in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(p.out) // terminal doesn't echo the user's Enter.
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	return p.line("")
}
