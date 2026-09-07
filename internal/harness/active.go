package harness

// Active is the harness config for the current redline run, loaded from the
// caller's checkout so worktrees without a committed .redline.yml still get
// worktree prepare steps. Set by run.Run; read by panes.
var Active *Config

// ActiveRoot is the repository root Active was loaded from. env.from paths
// are resolved here when they are absent from the tree under review.
var ActiveRoot string

// worktreePrepared records produce roots that already ran harness.worktree
// this run, so lint delta does not repeat stub-ui for head.
var worktreePrepared = map[string]bool{}

// ResetWorktreePrepared clears the worktree dedup map. Called at run end.
func ResetWorktreePrepared() {
	worktreePrepared = map[string]bool{}
}
