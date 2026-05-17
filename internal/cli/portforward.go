package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/portforward"
	"github.com/optimumsage/superkube/internal/ui"
)

func newPortForwardCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "pf",
		Aliases: []string{"port-forward"},
		Short:   "Manage background kubectl port-forwards",
		Long: `Run kubectl port-forward as a tracked background process.

Each forward gets a short id and a log file under your superkube state
directory. Liveness is checked on every ` + "`sk pf` invocation; stale entries are pruned automatically." + `

Subcommands:
  list           list active forwards (default when no subcommand)
  start          launch a forward in the background
  stop           kill a forward (or "all" to stop every active forward)
  logs           tail the log of a forward (-f follows)

Examples:
  sk pf start svc/web 8080:80
  sk pf start pod/api 5005:5005 -n staging
  sk pf
  sk pf logs pf-1abc -f
  sk pf stop pf-1abc
  sk pf stop all`,
		// No subcommand → list. Pass extra args along for forgiving UX.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("unknown pf subcommand %q; try `sk pf --help`", args[0])
			}
			return runPFList(cmd)
		},
	}
	c.AddCommand(newPFListCmd())
	c.AddCommand(newPFStartCmd())
	c.AddCommand(newPFStopCmd())
	c.AddCommand(newPFLogsCmd())
	return c
}

func newPFListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List active port-forwards",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPFList(cmd)
		},
	}
}

func newPFStartCmd() *cobra.Command {
	var namespace, address string
	c := &cobra.Command{
		Use:   "start TARGET PORT[:PORT]...",
		Short: "Start a port-forward in the background",
		Long: `TARGET is anything kubectl port-forward accepts: pod/foo, svc/web,
deploy/api, etc. Ports follow kubectl's "LOCAL:REMOTE" syntax (multiple are
allowed). The forward inherits your --context, --namespace, and --kubeconfig
from sk's root flags unless overridden inline.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]
			ports := args[1:]
			ns := namespace
			if ns == "" {
				ns = Flags.Namespace
			}
			entry, err := portforward.Start(portforward.StartOpts{
				Target:     target,
				Ports:      ports,
				Namespace:  ns,
				Context:    Flags.Context,
				Kubeconfig: Flags.Kubeconfig,
				Address:    address,
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(),
				ui.Render(ui.Success, fmt.Sprintf("started %s (pid %d): %s %s",
					entry.ID, entry.PID, entry.Target, strings.Join(entry.Ports, " "))))
			fmt.Fprintln(cmd.OutOrStdout(), ui.Render(ui.Subtle, "logs: "+entry.LogPath))
			return nil
		},
	}
	c.Flags().StringVarP(&namespace, "namespace", "n", "", "namespace (overrides sk --namespace)")
	c.Flags().StringVar(&address, "address", "", "bind address (passes through to kubectl --address)")
	return c
}

func newPFStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop ID|all",
		Short: "Terminate a tracked port-forward",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			stopped, err := portforward.Stop(args[0])
			if err != nil {
				return err
			}
			if len(stopped) == 0 {
				return fmt.Errorf("no port-forward matched %q", args[0])
			}
			for _, e := range stopped {
				fmt.Fprintln(cmd.OutOrStdout(),
					ui.Render(ui.Success, fmt.Sprintf("stopped %s (pid %d): %s", e.ID, e.PID, e.Target)))
			}
			return nil
		},
	}
}

func newPFLogsCmd() *cobra.Command {
	var follow bool
	c := &cobra.Command{
		Use:   "logs ID",
		Short: "Print the log file for a tracked port-forward",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entry, ok, err := portforward.FindByID(args[0])
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no port-forward with id %q", args[0])
			}
			return streamPFLog(cmd, entry, follow)
		},
	}
	c.Flags().BoolVarP(&follow, "follow", "f", false, "follow log output as it's written")
	return c
}

func runPFList(cmd *cobra.Command) error {
	entries, err := portforward.Load()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if len(entries) == 0 {
		fmt.Fprintln(out, "(no active port-forwards)")
		return nil
	}
	headers := []string{"ID", "TARGET", "PORTS", "NS", "CTX", "PID", "AGE"}
	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, []string{
			e.ID,
			e.Target,
			strings.Join(e.Ports, ","),
			e.Namespace,
			e.Context,
			fmt.Sprintf("%d", e.PID),
			humanAge(time.Since(e.StartedAt)),
		})
	}
	ui.PrintTable(out, headers, rows)
	return nil
}

func streamPFLog(cmd *cobra.Command, entry portforward.Entry, follow bool) error {
	f, err := os.Open(entry.LogPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(cmd.OutOrStdout(), "(no log output yet)")
			return nil
		}
		return err
	}
	defer f.Close()

	if _, err := io.Copy(cmd.OutOrStdout(), f); err != nil {
		return err
	}
	if !follow {
		return nil
	}
	pos, _ := f.Seek(0, io.SeekCurrent)
	for {
		select {
		case <-cmd.Context().Done():
			return nil
		case <-time.After(300 * time.Millisecond):
		}
		info, err := os.Stat(entry.LogPath)
		if err != nil || info.Size() <= pos {
			continue
		}
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return err
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			fmt.Fprintln(cmd.OutOrStdout(), scanner.Text())
		}
		pos = info.Size()
	}
}

func humanAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
