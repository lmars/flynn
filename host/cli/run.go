package cli

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/docker/docker/pkg/term"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/cliutil"
	"github.com/flynn/flynn/pkg/cluster"
	"github.com/flynn/flynn/pkg/exec"
	"github.com/flynn/go-docopt"
)

func init() {
	Register("run", runRun, `
usage: flynn-host run [options] [--] <artifact> <command> [<argument>...]

Run an interactive job.

Options:
	--host=<host>        run on a specific host
	--bind=<mountspecs>  bind mount a directory into the job (ex: /foo:/data,/bar:/baz)
	--volume=<path>      mount a temporary volume at <path>
	--keep-env=<vars>    keep env vars

Example:
	$ flynn-host run <(jq '.mongodb' images.json) mongo --version
	MongoDB shell version: 3.2.9
`)
}

func runRun(args *docopt.Args, client *cluster.Client) error {
	artifact := &ct.Artifact{}
	if err := cliutil.DecodeJSONArg(args.String["<artifact>"], artifact); err != nil {
		return err
	}
	cmd := exec.Cmd{
		ImageArtifact: artifact,
		Job: &host.Job{
			Config: host.ContainerConfig{
				Args:       append([]string{args.String["<command>"]}, args.All["<argument>"].([]string)...),
				TTY:        term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd()),
				Stdin:      true,
				DisableLog: true,
			},
		},
		HostID: args.String["--host"],
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	if keepEnv := args.String["--keep-env"]; keepEnv != "" {
		keys := strings.Split(keepEnv, ",")
		if cmd.Env == nil {
			cmd.Env = make(map[string]string, len(keys))
		}
		for _, key := range keys {
			println("===>", time.Now().Format("2006-01-02T15:04:05.999"), fmt.Sprintf("keepEnv %s %s", key, os.Getenv(key)))
			cmd.Env[key] = os.Getenv(key)
		}
	}
	if cmd.Job.Config.TTY {
		ws, err := term.GetWinsize(os.Stdin.Fd())
		if err != nil {
			return err
		}
		cmd.TermHeight = ws.Height
		cmd.TermWidth = ws.Width
		if cmd.Env == nil {
			cmd.Env = make(map[string]string, 3)
		}
		cmd.Env["COLUMNS"] = strconv.Itoa(int(ws.Width))
		cmd.Env["LINES"] = strconv.Itoa(int(ws.Height))
		cmd.Env["TERM"] = os.Getenv("TERM")
	}
	if specs := args.String["--bind"]; specs != "" {
		mounts := strings.Split(specs, ",")
		cmd.Job.Config.Mounts = make([]host.Mount, len(mounts))
		for i, m := range mounts {
			s := strings.SplitN(m, ":", 2)
			cmd.Job.Config.Mounts[i] = host.Mount{
				Target:    s[0],
				Location:  s[1],
				Writeable: true,
			}
		}
	}
	if path := args.String["--volume"]; path != "" {
		cmd.Volumes = []*ct.VolumeReq{{
			Path:         path,
			DeleteOnStop: true,
		}}
	}

	var termState *term.State
	if cmd.Job.Config.TTY {
		var err error
		termState, err = term.MakeRaw(os.Stdin.Fd())
		if err != nil {
			return err
		}
		// Restore the terminal if we return without calling os.Exit
		defer term.RestoreTerminal(os.Stdin.Fd(), termState)
		go func() {
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, syscall.SIGWINCH)
			for range ch {
				ws, err := term.GetWinsize(os.Stdin.Fd())
				if err != nil {
					return
				}
				cmd.ResizeTTY(ws.Height, ws.Width)
				cmd.Signal(int(syscall.SIGWINCH))
			}
		}()
	}

	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		sig := <-ch
		cmd.Signal(int(sig.(syscall.Signal)))
		time.Sleep(10 * time.Second)
		cmd.Signal(int(syscall.SIGKILL))
	}()

	err := cmd.Run()
	if status, ok := err.(exec.ExitError); ok {
		if cmd.Job.Config.TTY {
			// The deferred restore doesn't happen due to the exit below
			term.RestoreTerminal(os.Stdin.Fd(), termState)
		}
		os.Exit(int(status))
	}
	return err
}
