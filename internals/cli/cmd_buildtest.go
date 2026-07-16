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
	"time"

	"github.com/canonical/go-flags"

	"github.com/canonical/pebble/client"
)

const cmdBuildTestSummary = "Build source with snapcraft and run spread tests"
const cmdBuildTestDescription = `
The build-test command uploads the source code from the specified directory,
builds it using snapcraft, and runs spread tests. The output includes the
stdout, stderr, and exit code from both the build and test steps.

If the build step fails, the test step is skipped.
`

type cmdBuildTest struct {
	client *client.Client

	Timeout time.Duration `long:"timeout"`
	Stream  bool          `long:"stream"`

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
