// Copyright (c) 2026 Canonical Ltd
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License version 3 as
// published by the Free Software Foundation.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cli

import (
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/canonical/go-flags"
	"golang.org/x/sys/unix"

	"github.com/canonical/pebble/client"
	"github.com/canonical/pebble/internals/logger"
	"github.com/canonical/pebble/internals/ptyutil"
)

const cmdBuildTestSummary = "Build source with snapcraft and run spread tests"
const cmdBuildTestDescription = `
The build-test command uploads the source code from the specified directory,
builds it using snapcraft, and runs spread tests. The output includes the
stdout, stderr, and exit code from both the build and test steps.

If the build step fails, the test step is skipped.

Use --debug to enable interactive mode, which allows you to send input
to the build and test processes (for example, to debug a failing build).
This allocates a pseudo-terminal and forwards signals like Ctrl-C.
`

type cmdBuildTest struct {
	client *client.Client

	Timeout time.Duration `long:"timeout"`
	Stream  bool          `long:"stream"`
	Debug   bool          `long:"debug"`

	Positional struct {
		SourcePath string `positional-arg-name:"<source-path>" required:"1"`
	} `positional-args:"yes"`
}

type buildTestFormat struct {
	Build client.BuildResult  `json:"build" yaml:"build"`
	Run   *client.RunResult   `json:"run,omitempty" yaml:"run,omitempty"`
}

func init() {
	AddCommand(&CmdInfo{
		Name:        "build-test",
		Summary:     cmdBuildTestSummary,
		Description: cmdBuildTestDescription,
		ArgsHelp: map[string]string{
			"--timeout": "Timeout for the overall build and test operation",
			"--stream":  "Stream build and test output in real-time",
			"--debug":   "Enable interactive debug mode (allocate PTY, forward stdin and signals)",
		},
		New: func(opts *CmdOptions) flags.Commander {
			return &cmdBuildTest{client: opts.Client}
		},
	})
}

func (cmd *cmdBuildTest) Execute(args []string) error {
	if len(args) > 0 {
		return ErrExtraArgs
	}

	opts := &client.BuildTestOptions{
		SourcePath: cmd.Positional.SourcePath,
		Timeout:    cmd.Timeout,
	}

	if cmd.Debug {
		// Debug mode implies streaming and interactive.
		opts.Stdout = Stdout
		opts.Stderr = Stderr
		opts.Stdin = Stdin
		opts.Interactive = true
		opts.Terminal = true

		// Detect if stdin/stdout are TTYs.
		stdoutIsTerminal := ptyutil.IsTerminal(unix.Stdout)
		stdinIsTerminal := ptyutil.IsTerminal(unix.Stdin)

		// Set terminal to raw mode if we have a TTY.
		var oldState *ptyutil.State
		if stdoutIsTerminal && stdinIsTerminal {
			var rawErr error
			oldState, rawErr = ptyutil.MakeRaw(unix.Stdin)
			if rawErr != nil {
				return fmt.Errorf("cannot change terminal to raw mode: %v", rawErr)
			}
		}

		// Start the interactive build-test process.
		process, err := cmd.client.BuildTestInteractive(opts)
		if err != nil {
			if oldState != nil {
				ptyutil.Restore(unix.Stdin, oldState)
			}
			return err
		}

		// Start the control goroutine to handle signals and window resizing.
		stopControl := make(chan struct{})
		defer close(stopControl)
		sighup := make(chan struct{})
		go buildTestControlHandler(process, stdoutIsTerminal, stopControl, sighup)

		// Wait for the process to finish or SIGHUP.
		var result *client.BuildTestResult
		select {
		case waitErr := <-func() chan error {
			ch := make(chan error, 1)
			go func() {
				r, err := process.Wait()
				if err != nil {
					ch <- err
					return
				}
				result = r
				ch <- nil
			}()
			return ch
		}():
			if waitErr != nil {
				if oldState != nil {
					ptyutil.Restore(unix.Stdin, oldState)
				}
				return waitErr
			}
		case <-sighup:
			if oldState != nil {
				ptyutil.Restore(unix.Stdin, oldState)
			}
			fmt.Fprintf(os.Stderr, "SIGHUP received, exiting\n")
			return nil
		}

		// Restore terminal BEFORE printing exit codes, so output is
		// properly formatted and flushed.
		if oldState != nil {
			ptyutil.Restore(unix.Stdin, oldState)
		}

		// Print exit codes.
		fmt.Fprintf(Stdout, "\nBUILD exit code: %d\n", result.Build.ExitCode)
		if result.Run != nil {
			fmt.Fprintf(Stdout, "RUN exit code: %d\n", result.Run.ExitCode)
		}

		// Return exit code from the run step if present, otherwise from build.
		if result.Run != nil && result.Run.ExitCode != 0 {
			panic(&exitStatus{result.Run.ExitCode})
		}
		if result.Build.ExitCode != 0 {
			panic(&exitStatus{result.Build.ExitCode})
		}
		return nil
	}

	if cmd.Stream {
		opts.Stdout = Stdout
		opts.Stderr = Stderr
	}

	result, err := cmd.client.BuildTest(opts)
	if err != nil {
		return err
	}

	if cmd.Stream {
		// When streaming, output was already printed in real-time.
		// Just print the exit codes.
		fmt.Fprintf(Stdout, "BUILD exit code: %d\n", result.Build.ExitCode)
		if result.Run != nil {
			fmt.Fprintf(Stdout, "RUN exit code: %d\n", result.Run.ExitCode)
		}
	} else {
		// Print the results.
		if result.Build.Retried {
			fmt.Fprintf(Stdout, "BUILD (retried after clean)\n")
		} else {
			fmt.Fprintf(Stdout, "BUILD\n")
		}
		fmt.Fprintf(Stdout, "  Exit code: %d\n", result.Build.ExitCode)
		printOutput("Stdout", result.Build.Stdout)
		printOutput("Stderr", result.Build.Stderr)

		if result.Run != nil {
			fmt.Fprintf(Stdout, "\nRUN\n")
			fmt.Fprintf(Stdout, "  Exit code: %d\n", result.Run.ExitCode)
			printOutput("Stdout", result.Run.Stdout)
			printOutput("Stderr", result.Run.Stderr)
		}
	}

	// Return exit code from the run step if present, otherwise from build.
	if result.Run != nil && result.Run.ExitCode != 0 {
		panic(&exitStatus{result.Run.ExitCode})
	}
	if result.Build.ExitCode != 0 {
		panic(&exitStatus{result.Build.ExitCode})
	}

	return nil
}

func printOutput(label, output string) {
	if output == "" {
		fmt.Fprintf(Stdout, "  %s:\n    (none)\n", label)
	} else {
		fmt.Fprintf(Stdout, "  %s:\n", label)
		for _, line := range splitLines(output) {
			fmt.Fprintf(Stdout, "    %s\n", line)
		}
	}
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			lines = append(lines, line)
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func buildTestControlHandler(process *client.BuildTestProcess, terminal bool, stop <-chan struct{}, sighup chan<- struct{}) {
	ch := make(chan os.Signal, 10)
	signal.Notify(ch,
		unix.SIGWINCH, unix.SIGHUP,
		unix.SIGTERM, unix.SIGINT, unix.SIGQUIT, unix.SIGABRT,
		unix.SIGTSTP, unix.SIGTTIN, unix.SIGTTOU, unix.SIGUSR1,
		unix.SIGUSR2, unix.SIGSEGV, unix.SIGCONT)

	for {
		var sig os.Signal
		select {
		case sig = <-ch:
		case <-stop:
			return
		}

		switch sig {
		case unix.SIGWINCH:
			if !terminal {
				logger.Debugf("Received SIGWINCH but not in terminal mode, ignoring")
				break
			}
			logger.Debugf("Received '%s' signal, updating window geometry", sig)
			width, height, err := ptyutil.GetSize(unix.Stdout)
			if err != nil {
				logger.Debugf("Cannot get terminal size: %v", err)
				break
			}
			logger.Debugf("Window size is now: %dx%d", width, height)
			err = process.SendResize(width, height)
			if err != nil {
				logger.Debugf("Cannot set terminal size: %v", err)
				break
			}
		case unix.SIGHUP:
			logger.Debugf("Received 'SIGHUP' signal, forwarding and exiting")
			err := process.SendSignal("SIGHUP")
			if err != nil {
				logger.Debugf("Cannot forward signal '%s': %v", sig, err)
				break
			}
			close(sighup)
		case unix.SIGTERM, unix.SIGINT, unix.SIGQUIT, unix.SIGABRT,
			unix.SIGTSTP, unix.SIGTTIN, unix.SIGTTOU, unix.SIGUSR1,
			unix.SIGUSR2, unix.SIGSEGV, unix.SIGCONT:
			logger.Debugf("Received '%s' signal, forwarding to build-test process", sig)
			err := process.SendSignal(unix.SignalName(sig.(unix.Signal)))
			if err != nil {
				logger.Debugf("Cannot forward signal '%s': %v", sig, err)
				break
			}
		}
	}
}
