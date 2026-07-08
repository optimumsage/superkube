package cli

import (
	"testing"
	"time"
)

func TestResolveAITimeout(t *testing.T) {
	defer func(v time.Duration) { Flags.AITimeout = v }(Flags.AITimeout)

	Flags.AITimeout = 0
	if got := resolveAITimeout(false); got != defaultAITimeout {
		t.Fatalf("auto (no tools) = %v, want %v", got, defaultAITimeout)
	}
	if got := resolveAITimeout(true); got != defaultAIToolsTimeout {
		t.Fatalf("auto (tools) = %v, want %v", got, defaultAIToolsTimeout)
	}

	Flags.AITimeout = 42 * time.Second
	if got := resolveAITimeout(false); got != 42*time.Second {
		t.Fatalf("explicit (no tools) = %v, want 42s", got)
	}
	if got := resolveAITimeout(true); got != 42*time.Second {
		t.Fatalf("explicit should win over tools default, got %v", got)
	}
}
