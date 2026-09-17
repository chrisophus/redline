package review

// The output schema. Redline owns this half of the prompt: what shape a
// review takes, what a finding may claim, and what silence looks like.
//
// Every property is required. A model that must emit a field cannot quietly
// drop the one that carries the correlation, and an empty array is a clearer
// answer than an absent key.

// The fields a review is made of, defined once. calls.go flattens them into
// the tools every pass calls, and they mirror findings.Review, which is the
// file a reviewer writes and Redline already knows how to render.

// outputSchema is the whole review in one object: the walkthrough and the
// findings together. Explore mode asks for it as its response format; every
// other pass answers with the calls in calls.go.
func outputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"overview", "files", "comments"},
		"properties": map[string]any{
			"overview": overviewSchema(),
			"files":    map[string]any{"type": "array", "items": fileSchema()},
			"comments": map[string]any{"type": "array", "items": commentSchema()},
		},
	}
}

// cohortSchema is one group of files and what they do together. The summary is
// what the other cohorts' calls are shown of this one, so it is written for a
// reader who cannot see these diffs, not as a label.
func cohortSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"name", "summary", "files"},
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Two or three words naming what this group of files does together.",
			},
			"summary": map[string]any{
				"type": "string",
				"description": "One or two sentences on what changed in this group, written for " +
					"a reviewer who is reading a different group and cannot see these lines.",
			},
			"files": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
				"description": "Repository-relative paths, exactly as they appear in the change. " +
					"Every file you were shown belongs to exactly one cohort, and no cohort is empty.",
			},
		},
	}
}

func overviewSchema() map[string]any {
	return map[string]any{
		"type":        "string",
		"description": "One or two paragraphs on what this change is and why it exists.",
	}
}

func commentSchema() map[string]any {
	comment := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required": []string{
			"file", "line", "severity", "confidence", "body", "question",
		},
		"properties": map[string]any{
			"file": map[string]any{
				"type":        "string",
				"description": "Repository-relative path, exactly as it appears in the change.",
			},
			"line": map[string]any{
				"type":        "integer",
				"description": "Line in the file at head. 0 when the remark has no single line.",
			},
			"severity": map[string]any{
				"type": "string",
				"enum": []string{"error", "warning", "info"},
				"description": "What happens if this finding is right, not how sure " +
					"you are - confidence carries that. error: it breaks at run time, " +
					"loses data, or ships a wrong answer to a user. warning: it is " +
					"wrong and someone pays for it later. info: worth knowing, nothing " +
					"breaks. A crash on an input the code can receive is error however " +
					"unsure you are that the input occurs.",
			},
			"confidence": map[string]any{
				"type": "string",
				"enum": []string{"high", "medium", "low"},
				"description": "How sure you are that the finding is true. Report it " +
					"either way, but low is not free: a low-confidence finding stays on " +
					"the report and is not posted to the pull request, so nobody who " +
					"could fix it is shown it. Use low when you genuinely could not " +
					"settle it from what you were given, not as a hedge on a claim the " +
					"lines in front of you support.",
			},
			"body": map[string]any{
				"type":        "string",
				"description": "The remark itself. One or two sentences, specific, no preamble.",
			},
			// Required, and required is the point. A model that has to say how
			// its claim could be checked writes fewer claims that cannot be,
			// and a schema enforces that where a prompt line asks for it and
			// is forgotten by the tenth comment.
			"question": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"kind", "ask", "subject"},
				"properties": map[string]any{
					"kind": map[string]any{
						"type": "string",
						"enum": []string{"diff", "precedent", "caller", "rule", "history", "type", "none"},
						"description": "What would settle this finding. " +
							"diff: what you were already shown settles it, nothing needs looking up. " +
							"precedent: whether this repository already does the same thing elsewhere. " +
							"caller: what calls or reads what changed. " +
							"rule: whether the team wrote a rule about this. " +
							"history: why the removed code was there. " +
							"type: what a type can represent. " +
							"none: nothing available would settle it, which means you are speculating.",
					},
					"ask": map[string]any{
						"type": "string",
						"description": "The question in one sentence, as you would ask a colleague " +
							"with the repository open.",
					},
					"subject": map[string]any{
						"type": "string",
						"description": "The one thing to look up: a symbol, a path, or a pattern. " +
							"Empty only when kind is diff or none.",
					},
				},
			},
		},
	}
	return comment
}

func fileSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"path", "summary"},
		"properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"summary": map[string]any{"type": "string", "description": "One line on what this file's change does."},
		},
	}
}
