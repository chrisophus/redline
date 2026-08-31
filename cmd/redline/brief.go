package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ccason/redline/internal/findings"
	"github.com/ccason/redline/internal/packet"
	"github.com/ccason/redline/internal/reviewer"
	"github.com/ccason/redline/internal/run"
)

// noBrief is what --brief takes to mean "skip the context pass". It exists so a
// configured default can be silenced for one run without editing config.
const noBrief = "none"

// runBrief runs the context-gathering adapter named by --brief and attaches
// what it found to the packet. It is modelled on withReviewer: an unknown name
// is a hard error the operator can fix before retrying; a brief that ran and
// failed is recorded as a failed substrate and an unknown, never raised. A
// brief that did not run must not read as a repository with no context.
//
// Default is off. Spending a model call from `redline review` has to be asked
// for, for the same reason `post` is never a side effect of `run`.
func runBrief(o opts, res *run.Result) error {
	name := strings.TrimSpace(o.brief)
	if name == "" || name == noBrief {
		return nil
	}

	adapters, err := reviewer.Load(o.out)
	if err != nil {
		return err
	}
	a, ok := adapters[name]
	if !ok {
		return fmt.Errorf("unknown brief %q (known: %s; add your own in %s)",
			name, strings.Join(adapterNames(adapters), ", "), filepath.Join(o.out, "reviewers.json"))
	}

	dir, err := reviewDir(res.Packet.Target)
	if err != nil {
		return err
	}
	tgt := reviewTarget(res.Packet.Target)
	files := briefFiles(res.Packet)

	status := newStatus(os.Stderr)
	started := time.Now()
	brief, runErr := reviewer.RunBrief(context.Background(), a, dir, tgt, o.out, files,
		reviewer.WithProgress(status.interval, status.update))
	if runErr != nil {
		res.Report.Substrates = append(res.Report.Substrates, findings.SubstrateStatus{
			Name:   "brief:" + a.Name,
			State:  findings.SubstrateFailed,
			Detail: runErr.Error(),
		})
		res.Report.Unknowns = append(res.Report.Unknowns, findings.Unknown{
			Substrate: "brief:" + a.Name,
			Message:   "the " + a.Name + " context brief did not run, so nothing outside the diff it would have found is in this packet",
			Reason:    runErr.Error(),
		})
		status.fail(a.Name, time.Since(started), runErr)
		return nil
	}
	status.done(a.Name, time.Since(started), briefCount(brief))

	res.Packet.Brief = brief
	res.Report.Substrates = append(res.Report.Substrates, findings.SubstrateStatus{
		Name:   "brief:" + a.Name,
		State:  findings.SubstrateRan,
		Detail: briefDetail(brief),
	})
	return nil
}

// briefFiles is the changed-path list the brief prompt expands into {files}.
func briefFiles(p *packet.Packet) string {
	if p == nil || len(p.Files) == 0 {
		return "(no changed files)"
	}
	var b strings.Builder
	for i, f := range p.Files {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(f.Path)
	}
	return b.String()
}

func briefCount(b *packet.Brief) int {
	if b == nil {
		return 0
	}
	return len(b.References) + len(b.DocsNaming) + len(b.TestsCovering)
}

func briefDetail(b *packet.Brief) string {
	if b == nil {
		return "empty brief"
	}
	return fmt.Sprintf("%d references, %d docs, %d tests, %d files read",
		len(b.References), len(b.DocsNaming), len(b.TestsCovering), len(b.FilesRead))
}
