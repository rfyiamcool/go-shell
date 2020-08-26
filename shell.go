package shell

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/errors"
)

var (
	ErrLineBufferOverflow = errors.New("line buffer overflow")

	ErrAlreadyFinished      = errors.New("already finished")
	ErrNotFoundCommand      = errors.New("command not found")
	ErrNotExecutePermission = errors.New("not execute permission")
	ErrInvalidArgs          = errors.New("Invalid argument to exit")
	ErrProcessTimeout       = errors.New("throw process timeout")
	ErrProcessCancel        = errors.New("active cancel process")

	DefaultExitCode = 2
)

type Cmd struct {
	ctx    context.Context
	cancel context.CancelFunc

	stdcmd *exec.Cmd

	sync.Mutex

	Bash      string
	ShellMode bool
	Status    Status
	Env       []string
	Dir       string

	isFinalized bool

	timeout int

	statusChan chan Status
	doneChan   chan error

	output bytes.Buffer // stdout + stderr
	stdout bytes.Buffer
	stderr bytes.Buffer
}

type Status struct {
	PID      int
	Finish   bool
	ExitCode int
	Error    error
	CostTime time.Duration

	Output string // stdout + stderr
	Stdout string
	Stderr string

	startTime time.Time
	endTime   time.Time
}

type optionFunc func(*Cmd) error

// WithTimeout command timeout, unit second
func WithTimeout(td int) optionFunc {
	if td < 0 {
		panic("timeout > 0")
	}

	return func(o *Cmd) error {
		o.timeout = td
		return nil
	}
}

// WithShellMode set shell mode
func WithShellMode() optionFunc {
	return func(o *Cmd) error {
		o.ShellMode = true
		return nil
	}
}

// WithExecMode set exec mode, example: ["curl", "-i", "-v", "xiaorui.cc"]
func WithExecMode(b bool) optionFunc {
	return func(o *Cmd) error {
		o.ShellMode = false
		return nil
	}
}

// WithSetDir set work dir
func WithSetDir(dir string) optionFunc {
	return func(o *Cmd) error {
		o.Dir = dir
		return nil
	}
}

// WithSetEnv set env
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
		ShellMode:  true,
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
func (c *Cmd) Start() error {
	if c.Status.Finish {
		return ErrAlreadyFinished
	}

	return c.run()
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

func (c *Cmd) buildCtx() {
	if c.timeout > 0 {
		c.ctx, c.cancel = context.WithTimeout(context.Background(), time.Duration(c.timeout)*time.Second)
	} else {
		c.ctx, c.cancel = context.WithCancel(context.Background())
	}
}

func (c *Cmd) run() error {
	var (
		cmd *exec.Cmd

		sysProcAttr *syscall.SysProcAttr
	)

	c.buildCtx()

	sysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	c.Status.startTime = time.Now()
	if c.ShellMode {
		cmd = exec.Command("bash", "-c", c.Bash)
	} else {
		args := strings.Split(c.Bash, " ")
		cmd = exec.Command(args[0], args[1:]...)
	}

	cmd.Dir = c.Dir
	cmd.Env = c.Env
	cmd.SysProcAttr = sysProcAttr

	// merge multi writer
	mergeStdout := io.MultiWriter(&c.output, &c.stdout)
	mergeStderr := io.MultiWriter(&c.output, &c.stderr)

	// reset writer
	cmd.Stdout = mergeStdout
	cmd.Stderr = mergeStderr
	c.stdcmd = cmd

	// async start
	err := c.stdcmd.Start()
	if err != nil {
		c.Status.Error = err
		return err
	}

	go c.handleWait()

	return nil
}

func (c *Cmd) handleWait() error {
	defer func() {
		if c.Status.Finish {
			return
		}
		c.statusChan <- c.Status
		c.finalize()
	}()

	c.handleTimeout()

	// join process
	err := c.stdcmd.Wait()
	if c.ctx.Err() == context.DeadlineExceeded {
		return err
	}
	if c.ctx.Err() == context.Canceled {
		return err
	}

	if err != nil {
		c.Status.Error = formatExitCode(err)
		return err
	}

	c.Status.Stdout = c.stdout.String()
	c.Status.Stderr = c.stderr.String()
	c.Status.Output = c.output.String()
	return nil
}

// handleTimeout if use commandContext timeout, can't match shell mode.
func (c *Cmd) handleTimeout() {
	if c.timeout <= 0 {
		return
	}

	call := func() {
		select {
		case <-c.doneChan:
			// safe exit

		case <-c.ctx.Done():
			if c.ctx.Err() == context.Canceled {
				c.Status.Error = ErrProcessCancel
			}
			if c.ctx.Err() == context.DeadlineExceeded {
				c.Status.Error = ErrProcessTimeout
			}
			c.Stop()
		}
	}

	time.AfterFunc(time.Duration(c.timeout)*time.Second, call)
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
	c.isFinalized = true
}

// Stop kill -9 pid
func (c *Cmd) Stop() {
	if c.stdcmd == nil || c.stdcmd.Process == nil {
		return
	}

	c.cancel()
	c.finalize()
	c.stdcmd.Process.Kill()
	syscall.Kill(-c.stdcmd.Process.Pid, syscall.SIGKILL)
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

	return err
}

// CheckCmdExists check command in the PATH
func CheckCmdExists(cmd string) bool {
	_, err := exec.LookPath(cmd)
	if err != nil {
		return false
	} else {
		return true
	}
}

// CheckPnameRunning easy method
func CheckPnameRunning(pname string) bool {
	out, _, _ := CommandFormat("ps aux | grep %s |grep -v grep", pname)
	if strings.Contains(out, pname) {
		return true
	}
	return false
}

// Command easy command, return CombinedOutput, exitcode, err
func Command(args string) (string, int, error) {
	cmd := exec.Command("bash", "-c", args)
	outbs, err := cmd.CombinedOutput()
	out := string(outbs)
	return out, cmd.ProcessState.ExitCode(), err
}

// Command easy command format, return CombinedOutput, exitcode, err
func CommandFormat(format string, vals ...interface{}) (string, int, error) {
	sh := fmt.Sprintf(format, vals...)
	return Command(sh)
}

// CommandContains easy command, then match output with multi substr
func CommandContains(args string, subs ...string) bool {
	outbs, _, err := Command(args)
	if err != nil {
		return false
	}

	out := string(outbs)
	for _, sub := range subs {
		if !strings.Contains(out, sub) {
			return false
		}
	}
	return true
}

// CommandScript write script to random fname in /tmp directory and bash execute
func CommandScript(script []byte) (string, int, error) {
	fpath := fmt.Sprintf("/tmp/go-shell-%s", randString(16))
	defer os.RemoveAll(fpath)

	err := ioutil.WriteFile(fpath, script, 0666)
	if err != nil {
		return "", DefaultExitCode, errors.Errorf("dump script to file failed, err: %s", err.Error())
	}

	out, code, err := CommandFormat("bash %s", fpath)
	return out, code, err
}

// CommandWithMultiOut run command and return multi result; return string(stdout), string(stderr), exidcode, err
func CommandWithMultiOut(cmd string) (string, string, int, error) {
	var (
		stdout, stderr bytes.Buffer
		err            error
	)

	runner := exec.Command("bash", "-c", cmd)
	runner.Stdout = &stdout
	runner.Stderr = &stderr
	err = runner.Start()
	if err != nil {
		return string(stdout.Bytes()), string(stderr.Bytes()), runner.ProcessState.ExitCode(), err
	}

	err = runner.Wait()
	return string(stdout.Bytes()), string(stderr.Bytes()), runner.ProcessState.ExitCode(), err
}

// CommandWithChan return result queue
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
