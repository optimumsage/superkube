package cli

import (
	"github.com/spf13/cobra"

	"github.com/optimumsage/superkube/internal/ai"
	"github.com/optimumsage/superkube/internal/audit"
	"github.com/optimumsage/superkube/internal/guardrail"
	"github.com/optimumsage/superkube/internal/helm"
	"github.com/optimumsage/superkube/internal/kube"
	"github.com/optimumsage/superkube/internal/kubectl"
	"github.com/optimumsage/superkube/internal/web"
)

// newWebCmd registers `sk web`, the browser-based mirror of the CLI. The
// server runs in-process so it shares the same kube.Loader, kubectl.Runner,
// guardrail.Policy, audit log, and AI provider resolution.
func newWebCmd() *cobra.Command {
	var (
		port   int
		bind   string
		token  string
		noOpen bool
	)
	c := &cobra.Command{
		Use:   "web",
		Short: "Launch the browser-based control plane",
		Long: `Start a local HTTP server that mirrors every sk feature in a browser.

Defaults bind to 127.0.0.1 with no auth — safe for a personal kube tool. Use
--bind 0.0.0.0 only on trusted networks; a token is auto-generated unless you
pass --token explicitly.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			provider, _ := ai.Detect(Flags.AIProvider)
			runner, err := kubectl.Default()
			if err != nil {
				return err
			}
			// helm is optional — nil signals "not installed" to the web layer,
			// which then renders a friendly notice instead of failing.
			helmR, _ := helm.Default()
			loader := kube.Loader{
				KubeconfigPath: Flags.Kubeconfig,
				Context:        Flags.Context,
			}
			deps := web.Deps{
				Loader:     loader,
				Runner:     runner,
				Helm:       helmR,
				Policy:     func() guardrail.Policy { return Policy() },
				Banner:     ActiveBanner,
				Audit:      audit.Record,
				AIProvider: func() (ai.Provider, error) { return ai.Detect(Flags.AIProvider) },
				Context:    Flags.Context,
				Namespace:  Flags.Namespace,
				Kubeconfig: Flags.Kubeconfig,
				NoContext:  Flags.NoContext,
				Yes:        Flags.Yes,
			}
			_ = provider
			srv, err := web.New(web.Config{
				Bind: bind, Port: port, Token: token, NoOpen: noOpen,
			}, deps)
			if err != nil {
				return err
			}
			return srv.Run(cmd.Context())
		},
	}
	c.Flags().IntVar(&port, "port", 0, "TCP port (0 picks a free port)")
	c.Flags().StringVar(&bind, "bind", "127.0.0.1", "bind address; use 0.0.0.0 for non-loopback only on trusted networks")
	c.Flags().StringVar(&token, "token", "", "auth token; auto-generated when --bind is non-loopback")
	c.Flags().BoolVar(&noOpen, "no-open", false, "do not auto-open a browser")
	return c
}
