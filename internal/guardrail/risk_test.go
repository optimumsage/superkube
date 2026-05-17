package guardrail

import "testing"

func TestClassifyContext(t *testing.T) {
	cases := []struct {
		name string
		ctx  string
		want RiskLevel
	}{
		{"empty", "", RiskNone},
		{"plain prod token", "prod", RiskCritical},
		{"prod prefix", "prod-eu-west-1", RiskCritical},
		{"prod infix", "eu-prod-1", RiskCritical},
		{"prod suffix", "aws/cluster-prod", RiskCritical},
		{"production word", "company-production", RiskCritical},
		{"prd shortcode", "company-prd", RiskCritical},
		{"live env", "live-cluster", RiskCritical},
		{"mainnet", "mainnet", RiskCritical},
		{"colon separator (ARN-ish)", "arn:aws:eks:us-east-1:123:cluster/prod-payments", RiskCritical},

		{"staging", "staging", RiskRisky},
		{"qa name", "qa-eu", RiskRisky},
		{"canary", "release-canary", RiskRisky},

		// Critical wins over risky when both substrings present.
		{"both prod and staging", "prod-staging-bridge", RiskCritical},

		// Should NOT match — substrings inside larger words.
		{"product-staging is not prod", "product-staging", RiskRisky}, // staging matches risky here
		{"productivity is not prod", "productivity", RiskNone},
		{"prodigy is not prod", "prodigy", RiskNone},
		{"live-stock is not live (no separator after)", "livestock", RiskNone},

		// Random dev env should be none.
		{"plain dev", "dev", RiskNone},
		{"local kind", "kind-test-cluster", RiskRisky}, // "test" hits risky
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyContext(tc.ctx)
			if got != tc.want {
				t.Errorf("ClassifyContext(%q) = %v, want %v", tc.ctx, got, tc.want)
			}
		})
	}
}

func TestAutoBannerLabel(t *testing.T) {
	if got := AutoBannerLabel(RiskNone, "anything"); got != "" {
		t.Errorf("RiskNone should yield empty label, got %q", got)
	}
	if got := AutoBannerLabel(RiskCritical, "prod-eu"); got != "PRODUCTION CONTEXT: PROD-EU" {
		t.Errorf("critical label wrong: %q", got)
	}
	if got := AutoBannerLabel(RiskRisky, "staging"); got != "non-prod risky context: staging" {
		t.Errorf("risky label wrong: %q", got)
	}
}
