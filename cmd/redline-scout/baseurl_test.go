package main

import (
	"testing"

	"github.com/chrisophus/redline/internal/review"
)

// The gateway's address and the metered caller id come from the environment
// when the flag does not carry them, so a scout configured in a committed
// .redline.yml does not have to name an internal endpoint in git.
func TestTheScoutReadsItsEndpointFromTheEnvironment(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "https://gateway.internal/v1")
	t.Setenv("OPENAI_USER", "ci")
	if got := scoutBaseURL(review.APIOpenAI, ""); got != "https://gateway.internal/v1" {
		t.Errorf("the scout did not read OPENAI_BASE_URL: %q", got)
	}
	if got := scoutUser(review.APIOpenAI, ""); got != "ci" {
		t.Errorf("the scout did not read OPENAI_USER: %q", got)
	}
	// The flag wins, so a config that names one endpoint is not quietly
	// redirected by a variable someone exported for something else.
	if got := scoutBaseURL(review.APIOpenAI, "https://named.example"); got != "https://named.example" {
		t.Errorf("the environment overrode --base-url: %q", got)
	}
}

// The Anthropic wire is left alone. Its SDK reads its own environment,
// including an `ant auth login` profile, and a second reader here would
// shadow a credential chain this program does not implement.
func TestTheAnthropicWireIsNotRedirectedByTheOpenAIVariables(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "https://gateway.internal/v1")
	t.Setenv("OPENAI_USER", "ci")
	if got := scoutBaseURL(review.APIAnthropic, ""); got != "" {
		t.Errorf("an Anthropic scout picked up OPENAI_BASE_URL: %q", got)
	}
	if got := scoutUser(review.APIAnthropic, ""); got != "" {
		t.Errorf("an Anthropic scout picked up OPENAI_USER: %q", got)
	}
}
