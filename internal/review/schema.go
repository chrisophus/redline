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
			"relatedFindings", "body",
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
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"overview", "files", "comments"},
		"properties": map[string]any{
			"overview": map[string]any{
				"type":        "string",
				"description": "One or two paragraphs on what this change is and why it exists.",
			},
			"files":    map[string]any{"type": "array", "items": file},
			"comments": map[string]any{"type": "array", "items": comment},
		},
	}
}
