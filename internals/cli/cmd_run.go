// Copyright (c) 2014-2020 Canonical Ltd
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
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/canonical/go-flags"

	"github.com/holmanb/buildtest/cmd"
	"github.com/holmanb/buildtest/internals/daemon"
	"github.com/holmanb/buildtest/internals/logger"
	"github.com/holmanb/buildtest/internals/overlord"
	"github.com/holmanb/buildtest/internals/reaper"
	"github.com/holmanb/buildtest/internals/systemd"
)

const cmdRunSummary = "Run the build-test daemon"
const cmdRunDescription = `
The run command starts {{.DisplayName}} and its HTTP API.
`

type sharedRunEnterOpts struct {
	CreateDirs bool   `long:"create-dirs"`
	HTTP       string `long:"http"`
	Verbose    bool   `short:"v" long:"verbose"`
}

var sharedRunEnterArgsHelp = map[string]string{
	"--create-dirs": "Create {{.DisplayName}} directory on startup if it doesn't exist",
	"--http":        `Start HTTP API listening on this address in "<address>:port" format (for example, ":4000", "192.0.2.0:4000", "[2001:db8::1]:4000") (also BUILDTEST_HTTP)`,
	"--verbose":     "Log all output to stdout (also BUILDTEST_VERBOSE=1)",
}

type cmdRun struct {
	socketPath string
	pebbleDir  string

	sharedRunEnterOpts
}

func init() {
	AddCommand(&CmdInfo{
		Name:        "run",
		Summary:     cmdRunSummary,
		Description: cmdRunDescription,
		ArgsHelp:    sharedRunEnterArgsHelp,
		New: func(opts *CmdOptions) flags.Commander {
			return &cmdRun{
				socketPath: opts.SocketPath,
				pebbleDir:  opts.PebbleDir,
			}
		},
	})
}

// newCmdRun creates a cmdRun with the given options, for testing.
func newCmdRun(http string) *cmdRun {
	return &cmdRun{
		sharedRunEnterOpts: sharedRunEnterOpts{
			HTTP: http,
		},
	}
}

func (rcmd *cmdRun) Execute(args []string) error {
	if len(args) > 0 {
		return ErrExtraArgs
	}

	rcmd.run(nil)

	return nil
}

// httpAddress returns the HTTP address for the daemon, using the --http
// flag if set, otherwise falling back to the BUILDTEST_HTTP environment variable.
func (rcmd *cmdRun) httpAddress() string {
	if rcmd.HTTP != "" {
		return rcmd.HTTP
	}
	return os.Getenv("BUILDTEST_HTTP")
}

func (rcmd *cmdRun) run(ready chan<- func()) {
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	if err := runDaemon(rcmd, sigs, ready); err != nil {
		switch {
		case errors.Is(err, daemon.ErrRestartSocket):
			// No "error: " prefix as this isn't an error.
			fmt.Fprintf(os.Stdout, "%v\n", err)
			// This exit code must be in system'd SuccessExitStatus.
			panic(&exitStatus{42})
		case errors.Is(err, daemon.ErrRestartServiceFailure):
			// Daemon returns distinct code for service-failure shutdown.
			panic(&exitStatus{10})
		case errors.Is(err, daemon.ErrRestartCheckFailure):
			// Daemon returns distinct code for check-failure shutdown.
			panic(&exitStatus{11})
		}
		fmt.Fprintf(os.Stderr, "cannot run daemon: %v\n", err)
		panic(&exitStatus{1})
	}
}

func runWatchdog(d *daemon.Daemon) (*time.Ticker, error) {
	if os.Getenv("WATCHDOG_USEC") == "" {
		// Not running under systemd.
		return nil, nil
	}
	usec, err := strconv.ParseFloat(os.Getenv("WATCHDOG_USEC"), 64)
	if usec == 0 || err != nil {
		return nil, fmt.Errorf("cannot parse WATCHDOG_USEC: %q", os.Getenv("WATCHDOG_USEC"))
	}
	dur := time.Duration(usec/2) * time.Microsecond
	logger.Debugf("Setting up sd_notify() watchdog timer every %s", dur)
	wt := time.NewTicker(dur)

	go func() {
		for {
			select {
			case <-wt.C:
				// TODO: poke the API here and only report WATCHDOG=1 if it
				//       replies with valid data.
				systemd.SdNotify("WATCHDOG=1")
			case <-d.Dying():
				return
			}
		}
	}()

	return wt, nil
}

var checkRunningConditionsRetryDelay = 300 * time.Second

func sanityCheck() error {
	// Nothing interesting to check for now. See snapd's sanity package for examples.
	return nil
}

func runDaemon(rcmd *cmdRun, ch chan os.Signal, ready chan<- func()) error {
	err := reaper.Start()
	if err != nil {
		return fmt.Errorf("cannot start child process reaper: %w", err)
	}
	defer func() {
		err := reaper.Stop()
		if err != nil {
			logger.Noticef("Cannot stop child process reaper: %v", err)
		}
	}()

	t0 := time.Now().Truncate(time.Millisecond)

	if rcmd.CreateDirs {
		err := os.MkdirAll(rcmd.pebbleDir, 0o700)
		if err != nil {
			return err
		}
	}
	err = maybeCopyPebbleDir(rcmd.pebbleDir, getCopySource())
	if err != nil {
		return err
	}

	dopts := daemon.Options{
		Dir:         rcmd.pebbleDir,
		SocketPath:  rcmd.socketPath,
		HTTPAddress: rcmd.httpAddress(),
	}
	if os.Getenv("BUILDTEST_VERBOSE") == "1" || rcmd.Verbose {
		dopts.ServiceOutput = os.Stdout
	}
	if os.Getenv("BUILDTEST_PERSIST") == "never" {
		dopts.Persist = overlord.PersistNever
	}

	d, err := daemon.New(&dopts)
	if err != nil {
		return err
	}
	if err := d.Init(); err != nil {
		return err
	}

	// Run sanity check now, if anything goes wrong with the
	// check we go into "degraded" mode where we always report
	// the given error to any client.
	var checkTicker <-chan time.Time
	var tic *time.Ticker
	if err := sanityCheck(); err != nil {
		degradedErr := fmt.Errorf("system is not healthy: %s", err)
		logger.Noticef("%s", degradedErr)
		d.SetDegradedMode(degradedErr)
		tic = time.NewTicker(checkRunningConditionsRetryDelay)
		checkTicker = tic.C
	}

	d.Version = cmd.Version
	if err := d.Start(); err != nil {
		return err
	}

	watchdog, err := runWatchdog(d)
	if err != nil {
		return fmt.Errorf("cannot run software watchdog: %v", err)
	}
	if watchdog != nil {
		defer watchdog.Stop()
	}

	logger.Debugf("activation done in %v", time.Now().Truncate(time.Millisecond).Sub(t0))

	// The "stop" channel is used to stop the daemon externally.
	var stop chan struct{}
	if ready != nil {
		stop = make(chan struct{}, 1)
		notifyReady := func() {
			ready <- func() { close(stop) }
			close(ready)
		}
		notifyReady()
	}

out:
	for {
		select {
		case sig := <-ch:
			logger.Noticef("Exiting on %s signal.\n", sig)
			break out
		case <-d.Dying():
			// something called Stop()
			logger.Noticef("Server exiting! Reason: %v", d.Err())
			break out
		case <-checkTicker:
			if err := sanityCheck(); err == nil {
				d.SetDegradedMode(nil)
				tic.Stop()
			}
		case <-stop:
			break out
		}
	}

	return d.Stop(ch)
}

func maybeCopyPebbleDir(destDir, srcDir string) error {
	if srcDir == "" {
		return nil
	}
	_, err := os.Stat(srcDir)
	if errors.Is(err, os.ErrNotExist) {
		// Skip missing source directory.
		return nil
	} else if err != nil {
		return err
	}
	entries, err := os.ReadDir(destDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	} else if len(entries) != 0 {
		// Skip non-empty dir.
		return nil
	}
	fsys := os.DirFS(srcDir)
	err = os.CopyFS(destDir, fsys)
	if err != nil {
		return fmt.Errorf("cannot copy %q to %q: %w", srcDir, destDir, err)
	}
	return nil
}
