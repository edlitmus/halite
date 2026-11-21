package main

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/config"
)

// The loader says what an unreadable boolean becomes, and for quiesce
// that has to be what quiesceSetting does with it. The two live in
// different packages, and the node's own startup warning for this case
// was removed in favour of the loader's, so if they disagreed an operator
// would be told "true applies" by a node that runs everything, or the
// reverse.
func TestTheLoaderSaysWhatQuiesceDoesWithAnUnreadableValue(t *testing.T) {
	for _, value := range []string{"maybe", "1", "[on]"} {
		t.Run(value, func(t *testing.T) {
			cfg, err := config.Load(config.Node, config.LoadOptions{
				Path: writeConfig(t, t.TempDir(), "quiesce: "+value+"\n"),
			})
			if err != nil {
				t.Fatal(err)
			}

			var warned string
			for _, w := range cfg.Warnings {
				if strings.Contains(w, `"quiesce"`) {
					warned = w
				}
			}
			if warned == "" {
				t.Fatalf("no warning about quiesce: %q", cfg.Warnings)
			}
			said := strings.HasSuffix(warned, "true applies")
			if did := quiesceSetting(cfg); said != did {
				t.Errorf("the loader said %q and the node quiesced=%v", warned, did)
			}
		})
	}
}
