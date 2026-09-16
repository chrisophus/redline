package change

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// configNames are the spellings of the project file, in the order tried. The
// harness and the review instructions read the same file for the keys they
// own; this package owns exclude, because it is the code that drops the paths.
var configNames = []string{".redline.yml", ".redline.yaml"}

type excludeConfig struct {
	Exclude []string `yaml:"exclude"`
}

// LoadExclude reads the exclude list from .redline.yml at root. A missing
// file, or a file with no exclude key, returns nil and no error: a repository
// that never writes one gets the built-in rules and nothing else.
//
// Malformed YAML is an error rather than an empty list. Treating it as empty
// would review a tree the config meant to trim, quietly, and the run would
// look like an expensive config that did nothing.
func LoadExclude(root string) ([]string, error) {
	if root == "" {
		return nil, nil
	}
	for _, name := range configNames {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		var cfg excludeConfig
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return cfg.Exclude, nil
	}
	return nil, nil
}
