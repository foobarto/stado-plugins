# guidance

Official opt-in lifecycle guidance application for stado.

The host provides only bounded current-session facts, current-input and
fast-retrieval presence, and the exact registry/session ceiling. This plugin
owns classifiers, thresholds, wording, ordering, and the choice to recommend
learning, isolated research, or retained-agent coordination. Its only
lifecycle authority is `lifecycle:contribute:pre_llm`: it can append bounded
advisory context but cannot deny a turn, replace the model or system prompt, or
mutate conversation history.

The generic session snapshot contains active signal, retained-child, and
unread-message facts. It deliberately does not expose another application's
learn/review journal, so this package never claims that a signal was reviewed
or suppresses advice from application-private completion state.

The v1 host surface is the interactive TUI only. Installation is not automatic;
the package must be installed through the normal signed-plugin trust flow and
explicitly listed as a background plugin.

`build.sh` produces an unsigned development artifact. It never creates a
signature and is not a publication path.
