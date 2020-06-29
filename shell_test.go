package shell

import (
	"fmt"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRunShell(t *testing.T) {
	cmd := NewCommand("ls;ls -sdf8;sleep 2;echo 123456")
	cmd.Start()
	cmd.Wait()
	status := cmd.Status

	fmt.Printf("finish: %v \n", status.Finish)
	fmt.Printf("output:\n%s \n------\n", status.Output)
	fmt.Printf("stdout:\n%s \n------\n", status.Stdout)
	fmt.Printf("stderr:\n%s \n------\n", status.Stderr)

	assert.Equal(t, status.ExitCode, 0)
	assert.Equal(t, status.Error, nil)
	assert.Equal(t, status.Finish, true)
	assert.Greater(t, status.PID, 0)
	assert.GreaterOrEqual(t, cmd.Status.CostTime.Seconds(), float64(2))
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
	cmd := NewCommand("echo 123; sleep 5", WithTimeout(2))
	cmd.Start()
	cmd.Wait()
	status := cmd.Status

	assert.Equal(t, status.Error, ErrProcessTimeout)
	assert.Greater(t, status.CostTime.Seconds(), float64(2))
	assert.Less(t, status.CostTime.Seconds(), float64(3))
}

func TestCheckStdout(t *testing.T) {
	cmd := NewCommand("echo 123123")
	cmd.Run()
	status := cmd.Status

	assert.Equal(t, status.Stdout, "123123\n")
	assert.Equal(t, status.Output, "123123\n")
	assert.Equal(t, status.Stderr, "")
}

func TestCheckOutput(t *testing.T) {
	cmd := NewCommand("echo 123123 >&2")
	cmd.Run()
	status := cmd.Status

	assert.Equal(t, status.Output, "123123\n")
	assert.Equal(t, status.Stdout, "")
	assert.Equal(t, status.Stderr, "123123\n")
}

func TestCheckExit127(t *testing.T) {
	cmd := NewCommand("xiaorui.cc")
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
