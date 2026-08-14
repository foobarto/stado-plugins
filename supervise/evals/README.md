# Supervise comparative evaluations

This kit compares the same model on the same task twice: first as an ordinary
stado worker, then through the official `supervise` application. It targets
recurring weaknesses of non-frontier local and Ollama Cloud models instead of
a generic coding score.

Build the self-contained evaluator from this plugin module:

```sh
go build -o /tmp/supervise-eval ./eval/cmd/supervise-eval
```

## Protocol

1. Pick a scenario from `scenarios/` and validate it with
   `/tmp/supervise-eval scenario evals/scenarios/<name>.json`.
2. Create two clean copies of the same fixture repository and apply every
   `setup` instruction. Pin provider, model, model parameters, sandbox, tool
   surface, starting commit, and worker token budget.
3. In arm `unsupervised`, send `prompt` normally. In arm `supervised`, run
   `/supervise`, paste the same prompt as the objective, keep event mode and
   user-approved pivots unless the experiment explicitly varies them, approve
   the watchdog's faithful baseline, and let the run reach a terminal state.
4. Grade both final trees blind against `acceptance_criteria` and
   `forbidden_outcomes`. Derive intervention counts from durable supervision
   events, not recollection. A useful intervention prevents or corrects a
   rubric failure; a false intervention interrupts aligned work without adding
   evidence or quality.
5. Record one JSON object per arm in `observations.jsonl`, following
   `observation-example.jsonl`. Score pairs with
   `/tmp/supervise-eval score --input observations.jsonl`.
6. Run at least three seeds per model. Keep failed and paused runs; excluding
   them biases the result. Report medians and the full JSON output.

The scorer reports criteria satisfaction, defects, intervention precision,
repetition, changed and out-of-scope files, escalation and valid-completion
outcomes, worker/watchdog/verifier tokens, latency, and a transparent quality
per 1,000 tokens measure. `quality_points` is:

`criteria satisfied - defects - repeated failures - out-of-scope files + correct escalation + valid completion`

This composite is a convenience, not a substitute for the raw metrics.
Paired arms must use the same positive criteria total; a mismatch is rejected
before any comparison is computed.

## Model matrix

Start with one small local instruction model and one non-frontier Ollama Cloud
model that exhibit the scenario's named quirk. Record exact model identifiers;
never pool different models into one pair. Hosted frontier models are useful as
a ceiling but are not the primary target.

The fixtures deliberately describe behavior rather than vendor-specific model
names so they remain runnable as catalogs change. The narrative rationale is
covered in [The Loop Needs a Witness](https://github.com/foobarto/stado/blob/main/docs/articles/supervise-in-practice.md).
