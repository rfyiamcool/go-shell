package shell

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFastStop(t *testing.T) {
	start := time.Now()
	cmd := NewCommand("sleep 5;echo 123;sleep 123")
	cmd.Start()
	cmd.Stop()
	end := time.Since(start)
	assert.Less(t, end.Seconds(), float64(2))
	time.Sleep(10 * time.Second)
}

func TestDelayStop(t *testing.T) {
	start := time.Now()
	cmd := NewCommand("sleep 5;echo 123;sleep 123")
	cmd.Start()

	go func() {
		time.Sleep(1 * time.Second)
		cmd.Stop()
	}()

	cmd.Wait()
	end := time.Since(start)
	assert.Less(t, end.Seconds(), float64(2))
}

func TestRunShell(t *testing.T) {
	cmd := NewCommand("ls;ls -sdf8;sleep 2;echo 123456")
	cmd.Start()
	cmd.Wait()
	status := cmd.Status

	assert.Equal(t, status.ExitCode, 0)
	assert.Equal(t, status.Error, nil)
	assert.Equal(t, status.Finish, true)
	assert.Greater(t, status.PID, 0)
	assert.GreaterOrEqual(t, cmd.Status.CostTime.Seconds(), float64(2))
}

func TestRunScript(t *testing.T) {
	js := `
	echo -n 1
	echo -n 2
	echo -n 3
	`
	cmd := NewCommand(js)
	cmd.Start()
	cmd.Wait()

	t.Log(cmd.Status.Output)
	assert.Equal(t, cmd.Status.Output, "123")
}

func TestRunScriptFile(t *testing.T) {
	js := `
	echo -n 1
	echo -n 2
	echo -n 3
	`
	fpath := "/tmp/go-shell-js-test"
	err := ioutil.WriteFile(fpath, []byte(js), os.ModePerm)
	assert.Nil(t, err, nil)
	out, _, _ := CommandFormat("bash %s", fpath)
	assert.Equal(t, out, "123")
}

func TestRunError(t *testing.T) {
	cmd := NewCommand("ls -sdf8")
	cmd.Start()
	cmd.Wait()
	status := cmd.Status

	fmt.Printf("error: %v \n", status.Error)
	fmt.Printf("exit code: %v \n", status.ExitCode)

	assert.NotEqual(t, status.ExitCode, 0)
	assert.NotEqual(t, status.Error, nil)
}

func TestRunTimeout(t *testing.T) {
	cmd := NewCommand("sleep 10;echo 111", WithTimeout(2), WithShellMode())
	cmd.Start()
	cmd.Wait()
	status := cmd.Status

	assert.Equal(t, status.Error, ErrProcessTimeout)
	assert.GreaterOrEqual(t, status.CostTime.Seconds(), float64(2))
	assert.Less(t, status.CostTime.Seconds(), float64(3))
}

func TestCheckStderr(t *testing.T) {
	cmd := NewCommand("echo -n \"123123\" >&2")
	cmd.Run() // start and wait
	status := cmd.Status

	assert.Equal(t, status.Stdout, "")
	assert.Equal(t, status.Output, "123123")
	assert.Equal(t, status.Stderr, "123123")
}

func TestCheckOutput1(t *testing.T) {
	cmd := NewCommand("lll sdf") // error command
	cmd.Run()                    // start and wait
	status := cmd.Status

	assert.Equal(t, status.Stdout, "")
	assert.NotEqual(t, status.Output, "")
	assert.NotEqual(t, status.Stderr, "")
}

func TestCheckOutput(t *testing.T) {
	cmd := NewCommand("echo -n 123123")
	cmd.Run() // start and wait
	status := cmd.Status

	assert.Equal(t, status.Output, "123123")
	assert.Equal(t, status.Stdout, "123123")
	assert.Equal(t, status.Stderr, "")
}

func TestCheckExit127(t *testing.T) {
	cmd := NewCommand("xiaorui.cc") // not exist command
	cmd.Run()
	assert.Equal(t, cmd.Status.Error, ErrNotFoundCommand)
}

func TestCheckStream(t *testing.T) {
	stdoutChan := make(chan string, 100)
	incr := 0
	go func() {
		for line := range stdoutChan {
			incr++
			fmt.Println(incr, line)
		}
	}()

	cmd := exec.Command("bash", "-c", "echo 123;sleep 1;echo 456; echo 789")
	stdout := NewOutputStream(stdoutChan)
	cmd.Stdout = stdout
	cmd.Run()

	assert.Equal(t, incr, 3)
}

func TestCheckBuffer(t *testing.T) {
	cmd := exec.Command("bash", "-c", "echo 123")
	stdout := NewOutputBuffer()
	cmd.Stdout = stdout
	cmd.Run()

	assert.Equal(t, stdout.buf.String(), "123\n")
	assert.Equal(t, stdout.Lines()[0], "123")
}

func TestCommand(t *testing.T) {
	out, code, err := Command("echo 123")
	assert.Equal(t, out, "123\n")
	assert.Equal(t, code, 0)
	assert.Equal(t, err, nil)

	out, code, err = Command("ls -sdf07979")
	assert.NotEqual(t, code, 0)
	assert.NotEqual(t, err, nil)
}

func TestCommandWithMultiOut(t *testing.T) {
	stdout, stderr, code, err := CommandWithMultiOut("echo 123 >&2")

	assert.Equal(t, stdout, "")
	assert.Equal(t, stderr, "123\n")
	assert.Equal(t, code, 0)
	assert.Equal(t, err, nil)
}

func TestCommandWithChan(t *testing.T) {
	queue := make(chan string, 10)
	err := CommandWithChan("echo 123;sleep 1;echo 456", queue)

	time.Sleep(1500 * time.Millisecond)
	assert.Equal(t, len(queue), 2)
	assert.Equal(t, err, nil)
}
