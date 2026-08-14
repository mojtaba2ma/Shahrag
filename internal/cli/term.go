package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

type terminalState struct {
	old unix.Termios
}

func getTerminalState(fd int) (*terminalState, error) {
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	return &terminalState{old: *t}, nil
}

func restoreTerminal(fd int, state *terminalState) {
	_ = unix.IoctlSetTermios(fd, unix.TCSETS, &state.old)
}

func readLineDisabled() (string, error) {
	fd := int(os.Stdin.Fd())
	t, err := getTerminalState(fd)
	if err != nil {
		in := bufio.NewReader(os.Stdin)
		s, _ := in.ReadString('\n')
		return strings.TrimRight(s, "\n"), nil
	}
	newState := t.old
	newState.Lflag &^= unix.ECHO
	_ = unix.IoctlSetTermios(fd, unix.TCSETS, &newState)
	defer restoreTerminal(fd, t)

	in := bufio.NewReader(os.Stdin)
	s, err := in.ReadString('\n')
	if err != nil {
		return "", err
	}
	fmt.Println()
	return strings.TrimRight(s, "\n"), nil
}
