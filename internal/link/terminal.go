package link

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

type prompt struct {
	pattern *regexp.Regexp
	answer  func() (string, error)
}

type terminal struct {
	master  *os.File
	command *exec.Cmd
	exited  chan error
}

const terminationTimeout = 5 * time.Second
const answerDelay = 150 * time.Millisecond // NOTE: Terminal flushes input written too early.
const drainDelay = 300 * time.Millisecond  // NOTE: Lets the reader drain output of exited process.
const interruptTimeout = time.Second       // NOTE: OpenVPN ignores SIGTERM at a prompt.

func startTerminal(command *exec.Cmd) (*terminal, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open pseudo terminal: %w", err)
	}
	dispose := true
	defer func() {
		if dispose {
			_ = master.Close()
		}
	}()
	unlock, number := int32(0), uint32(0)
	if err = control(master, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		return nil, fmt.Errorf("failed to unlock pseudo terminal: %w", err)
	} else if err = control(master, syscall.TIOCGPTN, unsafe.Pointer(&number)); err != nil {
		return nil, fmt.Errorf("failed to get pseudo terminal number: %w", err)
	}
	name := "/dev/pts/" + strconv.FormatUint(uint64(number), 10)
	slave, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open terminal '%v': %w", name, err)
	}
	defer slave.Close()
	command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err = command.Start(); err != nil {
		return nil, fmt.Errorf("failed to start '%v': %w", command.Path, err)
	}
	dispose = false
	result := &terminal{master: master, command: command, exited: make(chan error, 1)}
	return result, nil
}

func control(file *os.File, request uintptr, argument unsafe.Pointer) error {
	if _, _, code := syscall.Syscall(syscall.SYS_IOCTL, file.Fd(), request,
		uintptr(argument)); code != 0 {
		return code
	}
	return nil
}

func (terminal *terminal) pump(prompts []prompt, output func(line string)) {
	buffer, chunk, echo := []byte{}, make([]byte, 4096), ""
	waited := make(chan error, 1)
	go func() {
		err := terminal.command.Wait()
		time.Sleep(drainDelay)
		_ = terminal.master.Close()
		waited <- err
	}()
	var failure error
	for failure == nil {
		count, err := terminal.master.Read(chunk)
		buffer = append(buffer, chunk[:count]...)
		for {
			offset := bytes.IndexByte(buffer, '\n')
			if offset < 0 {
				break
			}
			if line := strings.TrimRight(string(buffer[:offset]), "\r"); line != echo || line == "" {
				output(line)
			}
			buffer, echo = buffer[offset+1:], ""
		}
		for _, item := range prompts {
			if !item.pattern.Match(buffer) {
				continue
			}
			output(strings.TrimSpace(string(buffer)))
			buffer = buffer[:0]
			if echo, failure = item.answer(); failure != nil {
				output("failed to answer prompt: " + failure.Error())
				break
			}
			time.Sleep(answerDelay)
			if _, failure = terminal.master.WriteString(echo + "\n"); failure != nil {
				output("failed to write answer: " + failure.Error())
			}
			break
		}
		if err != nil {
			break
		}
	}
	if err := terminal.finish(waited, failure != nil); failure != nil {
		terminal.exited <- failure
	} else if err != nil {
		terminal.exited <- fmt.Errorf("process '%v' exited: %w", terminal.command.Path, err)
	} else {
		terminal.exited <- errors.New("process exited")
	}
	close(terminal.exited)
}

func (terminal *terminal) finish(waited <-chan error, interrupt bool) error {
	if !interrupt {
		return <-waited
	}
	terminal.signal(syscall.SIGTERM)
	select {
	case err := <-waited:
		return err
	case <-time.After(interruptTimeout):
		terminal.signal(syscall.SIGKILL)
		return <-waited
	}
}

func (terminal *terminal) signal(signal syscall.Signal) {
	if terminal.command.Process != nil {
		_ = terminal.command.Process.Signal(signal)
	}
}

func (terminal *terminal) stop() {
	terminal.signal(syscall.SIGTERM)
	select {
	case <-terminal.exited:
	case <-time.After(terminationTimeout):
		terminal.signal(syscall.SIGKILL)
		<-terminal.exited
	}
}
