package review

// The output schema. Redline owns this half of the prompt: what shape a
// review takes, what a finding may claim, and what silence looks like.
//
// Every property is required. A model that must emit a field cannot quietly
// drop the one that carries the correlation, and an empty array is a clearer
// answer than an absent key.

// outputSchema is the JSON schema the model's response is constrained to. It
// mirrors findings.Review, which is the file a reviewer writes and Redline
// already knows how to render, so the producer emits the contract it will be
// read back through rather than a shape of its own.
func outputSchema() map[string]any {
	comment := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required": []string{
			"file", "line", "severity", "confidence", "category",
			"relatedFindings", "body", "question",
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
			},
			"confidence": map[string]any{
				"type": "string",
				"enum": []string{"high", "medium", "low"},
				"description": "How sure you are. Report the finding either way; " +
					"low confidence is folded away on the report, not discarded.",
			},
			"category": map[string]any{
				"type": "string",
				"enum": []string{"review", "correlation"},
				"description": "correlation when the finding connects two of the " +
					"prior findings you were given. review otherwise.",
			},
			"relatedFindings": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Ids of the prior findings this one builds on, exactly as given in brackets above. Empty for an ordinary remark.",
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
	file := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"path", "summary"},
		"properties": map[string]any{
			"path":    map[string]any{"type": "string"},
			"summary": map[string]any{"type": "string", "description": "One line on what this file's change does."},
		},
	}
	// verdict is a ruling on one finding a deterministic check already made.
	// The three words are the ones the report already renders and the skill
	// already documents for an agent writing review.json; this is the same
	// vocabulary reaching the same fields from the one command that calls a
	// model, rather than a second vocabulary meaning the same things.
	//
	// It is an array here and a map keyed by fingerprint on disk. A strict
	// output schema cannot describe an object whose keys are not known in
	// advance, and the fingerprints are not: parseReview does the conversion.
	verdict := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"finding", "ruling", "rationale", "fix"},
		"properties": map[string]any{
			"finding": map[string]any{
				"type":        "string",
				"description": "Id of the finding being ruled on, exactly as given in brackets above.",
			},
			"ruling": map[string]any{
				"type": "string",
				"enum": []string{"should-fix", "justified", "rule-noisy"},
				"description": "should-fix: the finding is right and the code should change. " +
					"justified: what it flags is deliberate and correct here. " +
					"rule-noisy: the check is wrong here, or fires too often to be worth reading.",
			},
			"rationale": map[string]any{
				"type":        "string",
				"description": "One line on why, naming what you saw that the check could not.",
			},
			"fix": map[string]any{
				"type":        "string",
				"description": "How to resolve it, one or two lines. Empty unless the ruling is should-fix.",
			},
		},
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"overview", "files", "comments", "verdicts"},
		"properties": map[string]any{
			"verdicts": map[string]any{"type": "array", "items": verdict},
			"overview": map[string]any{
				"type":        "string",
				"description": "One or two paragraphs on what this change is and why it exists.",
			},
			"files":    map[string]any{"type": "array", "items": file},
			"comments": map[string]any{"type": "array", "items": comment},
		},
	}
}
