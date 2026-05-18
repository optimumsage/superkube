package cli

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/service"
	"github.com/optimumsage/superkube/internal/ui"
)

// newWebStatusCmd registers `sk web status`, which reports whether the web
// service is installed/loaded/running and prints the URL + log paths from
// the sidecar JSON. Exit code is always 0 even when the service isn't
// installed — the table is the answer, not the exit code.
func newWebStatusCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "status",
		Short: "Show the status of the web UI background service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			mgr, err := service.NewManager()
			if err != nil {
				return err
			}
			label := webServiceLabelForPlatform()
			st, statusErr := mgr.Status(label)
			if statusErr != nil {
				return statusErr
			}
			sidecar, hasSidecar, _ := loadWebServiceState()

			out := cmd.OutOrStdout()
			rows := [][]string{
				{"Label", label},
				{"Installed", yesNo(st.Installed)},
				{"Loaded", yesNo(st.Loaded)},
				{"Running", yesNo(st.Running)},
			}
			if st.PID > 0 {
				rows = append(rows, []string{"PID", strconv.Itoa(st.PID)})
			}
			if st.UnitPath != "" {
				rows = append(rows, []string{"Unit", st.UnitPath})
			}
			if hasSidecar {
				rows = append(rows,
					[]string{"URL", webServiceURL(sidecar)},
					[]string{"Bind", sidecar.Bind},
					[]string{"Port", strconv.Itoa(sidecar.Port)},
				)
				if sidecar.Token != "" {
					rows = append(rows, []string{"Token", sidecar.Token})
				}
				if sidecar.LogPath != "" {
					rows = append(rows, []string{"Stdout log", sidecar.LogPath})
				}
				if sidecar.ErrLogPath != "" {
					rows = append(rows, []string{"Stderr log", sidecar.ErrLogPath})
				}
				rows = append(rows, []string{"Installed at", sidecar.InstalledAt.Local().Format("2006-01-02 15:04:05")})
			}
			ui.PrintTable(out, []string{"FIELD", "VALUE"}, rows)
			if !st.Installed {
				fmt.Fprintln(out, ui.Render(ui.Subtle, "run `sk web install` to register the service"))
			}
			return nil
		},
	}
	return c
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
