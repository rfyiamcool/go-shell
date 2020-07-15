package shell

type Yum struct {
	cmd     *Cmd
	pkg     string
	timeout int
}

type yumOption func(*Yum) error

func WithYumTimeout(timeout int) yumOption {
	return func(y *Yum) error {
		y.timeout = timeout
		return nil
	}
}

func NewYumCommand(pkg string, options ...yumOption) *Yum {
	yum := &Yum{
		pkg:     pkg,
		timeout: -1,
	}

	for _, opt := range options {
		opt(yum)
	}

	if yum.timeout > 0 {
		yum.cmd = NewCommand("yum install -y "+yum.pkg, WithShellMode(), WithTimeout(yum.timeout))
	} else {
		yum.cmd = NewCommand("yum install -y "+yum.pkg, WithShellMode())
	}

	return yum
}

func (y *Yum) YumInstallStart() {
	y.cmd.Start()
}

func (y *Yum) YumWait() (string, error) {
	y.cmd.Wait()
	status := y.cmd.Status

	if status.Error == nil && status.ExitCode == 0 {
		return status.Output, nil
	}
	return status.Output, status.Error
}

// YumInstall yum install synchorize.
func YumInstall(pkg string) (string, error) {
	out, code, err := Command("yum -y install " + pkg)
	if code == 1 && err != nil {
		return out, err
	}
	return out, nil
}

// yum remove pkg.
func YumRemove(pkg string) error {
	_, code, err := Command("yum -y remove " + pkg)
	if code == 0 && err == nil {
		return nil
	}
	return err
}

// YumInstallAsync Install asynchorize.
// Usage: YumInstallAsync("docker", WithTimeout(1)).Then(func(res string, err error){fmt.Println(res, err)})
func YumInstallAsync(pkg string, options ...yumOption) *Yum {
	yumCmd := NewYumCommand(pkg, options...)
	yumCmd.YumInstallStart()
	return yumCmd
}

func (y *Yum) Then(f func(string, error)) {
	go func(y *Yum) {
		res, err := y.YumWait()
		f(res, err)
	}(y)
}
