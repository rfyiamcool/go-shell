package shell

import (
	"bufio"
	"bytes"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/errors"
)

var (
	ErrLineBufferOverflow = errors.New("line buffer overflow")

	ErrNotFoundCommand      = errors.New("command not found")
	ErrNotExecutePermission = errors.New("not execute permission")
	ErrInvalidArgs          = errors.New("Invalid argument to exit")
	ErrProcessTimeout       = errors.New("throw process timeout")
)

type Cmd struct {
	stdcmd *exec.Cmd

	sync.Mutex

	Bash   string
	Status Status
	Env    []string
	Dir    string

	isFinalized bool

	timeout int

	statusChan chan Status
	doneChan   chan error
}

type Status struct {
	PID      int
	Finish   bool
	ExitCode int
	Error    error
	CostTime time.Duration
	Output   string

	Stdout string
	Stderr string

	startTime time.Time
	endTime   time.Time
}

type optionFunc func(*Cmd) error

func WithTimeout(td int) optionFunc {
	if td < 0 {
		panic("timeout > 0")
	}

	return func(o *Cmd) error {
		o.timeout = td
		return nil
	}
}

func WithSetDir(dir string) optionFunc {
	return func(o *Cmd) error {
		o.Dir = dir
		return nil
	}
}

func WithSetEnv(env []string) optionFunc {
	return func(o *Cmd) error {
		o.Env = env
		return nil
	}
}

func NewCommand(bash string, options ...optionFunc) *Cmd {
	c := &Cmd{
		Bash:       bash,
		Status:     Status{},
		statusChan: make(chan Status, 1),
		doneChan:   make(chan error, 1),
	}
	for _, opt := range options {
		opt(c)
	}
	return c
}

// Clone new Cmd with current config
func (c *Cmd) Clone() *Cmd {
	return NewCommand(c.Bash)
}

// Start async execute command
func (c *Cmd) Start() chan Status {
	if c.Status.Finish {
		return c.statusChan
	}

	go c.run()

	return c.statusChan
}

// Wait wait command finish
func (c *Cmd) Wait() error {
	<-c.doneChan
	return c.Status.Error
}

// Run start and wait process exit
func (c *Cmd) Run() error {
	c.Start()
	return c.Wait()
}

func (c *Cmd) run() error {
	var (
		cmd *exec.Cmd

		output bytes.Buffer
		stdout bytes.Buffer
		stderr bytes.Buffer
	)

	defer func() {
		c.statusChan <- c.Status
		c.finalize()
	}()

	c.Status.startTime = time.Now()
	cmd = exec.Command("bash", "-c", c.Bash)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	c.stdcmd = cmd

	// merge multi writer
	mergeStdout := io.MultiWriter(&output, &stdout)
	mergeStderr := io.MultiWriter(&output, &stderr)

	// reset writer
	cmd.Stdout = mergeStdout
	cmd.Stderr = mergeStderr

	// async start
	err := cmd.Start()
	if err != nil {
		c.Status.Error = err
		return err
	}

	c.hanldeTimeout()

	// join process
	err = cmd.Wait()
	if err != nil {
		c.Status.Error = formatExitCode(err)
		return err
	}

	c.Status.Stdout = stdout.String()
	c.Status.Stderr = stderr.String()
	c.Status.Output = output.String()

	return err
}

func (c *Cmd) hanldeTimeout() {
	if c.timeout <= 0 {
		return
	}

	timer := time.NewTimer(time.Duration(c.timeout) * time.Second)
	go func() {
		select {
		case <-c.doneChan:
			// self exit
		case <-timer.C:
			c.Stop()
			c.Status.Error = ErrProcessTimeout
		}
	}()
}

func (c *Cmd) finalize() {
	c.Lock()
	defer c.Unlock()

	if c.isFinalized {
		return
	}

	c.Status.CostTime = time.Now().Sub(c.Status.startTime)
	c.Status.Finish = true
	c.Status.PID = c.stdcmd.Process.Pid
	c.Status.ExitCode = c.stdcmd.ProcessState.ExitCode()

	// notify
	close(c.doneChan)
	close(c.statusChan)
}

// Stop kill -9 pid
func (c *Cmd) Stop() {
	if c.stdcmd.Process == nil {
		return
	}

	c.finalize()
	c.stdcmd.Process.Kill()
}

// Kill send custom signal to process
func (c *Cmd) Kill(sig syscall.Signal) {
	syscall.Kill(c.stdcmd.Process.Pid, sig)
}

// Cost
func (c *Cmd) Cost() time.Duration {
	return c.Status.CostTime
}

func formatExitCode(err error) error {
	if err == nil {
		return err
	}

	if strings.Contains(err.Error(), "exit status 127") {
		return ErrNotFoundCommand
	}
	if strings.Contains(err.Error(), "exit status 126") {
		return ErrNotExecutePermission
	}
	if strings.Contains(err.Error(), "exit status 128") {
		return ErrInvalidArgs
	}

	return nil
}

func CheckCmdExists(cmd string) bool {
	_, err := exec.LookPath(cmd)
	if err != nil {
		return false
	} else {
		return true
	}
}

func Command(args string) (string, int, error) {
	cmd := exec.Command("bash", "-c", args)
	outbs, err := cmd.CombinedOutput()
	out := string(outbs)
	return out, cmd.ProcessState.ExitCode(), err
}

func CommandWithMultiOut(cmd string, workPath string) (string, string, error) {
	var (
		stdout, stderr bytes.Buffer
		err            error
	)

	runner := exec.Command("bash", "-c", cmd)
	if workPath != "" {
		runner.Dir = workPath
	}
	runner.Stdout = &stdout
	runner.Stderr = &stderr
	err = runner.Start()
	if err != nil {
		return string(stdout.Bytes()), string(stderr.Bytes()), err
	}

	err = runner.Wait()
	return string(stdout.Bytes()), string(stderr.Bytes()), err
}

func CommandWithChan(cmd string, queue chan string) error {
	runner := exec.Command("bash", "-c", cmd)
	stdout, err := runner.StdoutPipe()
	if err != nil {
		return err
	}

	stderr, err := runner.StderrPipe()
	if err != nil {
		return err
	}

	runner.Start()

	call := func(in io.ReadCloser) {
		reader := bufio.NewReader(in)
		for {
			line, _, err := reader.ReadLine()
			if err != nil || io.EOF == err {
				break
			}

			select {
			case queue <- string(line):
			default:
			}
		}
	}

	go call(stdout)
	go call(stderr)

	runner.Wait()
	close(queue)
	return nil
}

type OutputBuffer struct {
	buf   *bytes.Buffer
	lines []string
	*sync.Mutex
}

func NewOutputBuffer() *OutputBuffer {
	out := &OutputBuffer{
		buf:   &bytes.Buffer{},
		lines: []string{},
		Mutex: &sync.Mutex{},
	}
	return out
}

func (rw *OutputBuffer) Write(p []byte) (n int, err error) {
	rw.Lock()
	n, err = rw.buf.Write(p) // and bytes.Buffer implements io.Writer
	rw.Unlock()
	return
}

func (rw *OutputBuffer) Lines() []string {
	rw.Lock()
	s := bufio.NewScanner(rw.buf)
	for s.Scan() {
		rw.lines = append(rw.lines, s.Text())
	}
	rw.Unlock()
	return rw.lines
}

type OutputStream struct {
	streamChan chan string
	bufSize    int
	buf        []byte
	lastChar   int
}

// NewOutputStream creates a new streaming output on the given channel.
func NewOutputStream(streamChan chan string) *OutputStream {
	out := &OutputStream{
		streamChan: streamChan,
		bufSize:    16384,
		buf:        make([]byte, 16384),
		lastChar:   0,
	}
	return out
}

// Write makes OutputStream implement the io.Writer interface.
func (rw *OutputStream) Write(p []byte) (n int, err error) {
	n = len(p) // end of buffer
	firstChar := 0

	for {
		newlineOffset := bytes.IndexByte(p[firstChar:], '\n')
		if newlineOffset < 0 {
			break // no newline in stream, next line incomplete
		}

		// End of line offset is start (nextLine) + newline offset. Like bufio.Scanner,
		// we allow \r\n but strip the \r too by decrementing the offset for that byte.
		lastChar := firstChar + newlineOffset // "line\n"
		if newlineOffset > 0 && p[newlineOffset-1] == '\r' {
			lastChar -= 1 // "line\r\n"
		}

		// Send the line, prepend line buffer if set
		var line string
		if rw.lastChar > 0 {
			line = string(rw.buf[0:rw.lastChar])
			rw.lastChar = 0 // reset buffer
		}
		line += string(p[firstChar:lastChar])
		rw.streamChan <- line // blocks if chan full

		// Next line offset is the first byte (+1) after the newline (i)
		firstChar += newlineOffset + 1
	}

	if firstChar < n {
		remain := len(p[firstChar:])
		bufFree := len(rw.buf[rw.lastChar:])
		if remain > bufFree {
			var line string
			if rw.lastChar > 0 {
				line = string(rw.buf[0:rw.lastChar])
			}
			line += string(p[firstChar:])
			err = ErrLineBufferOverflow
			n = firstChar
			return // implicit
		}
		copy(rw.buf[rw.lastChar:], p[firstChar:])
		rw.lastChar += remain
	}

	return // implicit
}

func (rw *OutputStream) Lines() <-chan string {
	return rw.streamChan
}

func (rw *OutputStream) SetLineBufferSize(n int) {
	rw.bufSize = n
	rw.buf = make([]byte, rw.bufSize)
}
