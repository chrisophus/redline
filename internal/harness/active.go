package harness

// Active is the harness config for the current redline run, loaded from the
// caller's checkout so worktrees without a committed .redline.yml still get
// worktree prepare steps. Set by run.Run; read by panes.
var Active *Config
