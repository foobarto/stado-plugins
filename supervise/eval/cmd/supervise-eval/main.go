package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	superviseeval "github.com/foobarto/stado-plugins/supervise/eval"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "supervise-eval:", err)
		os.Exit(2)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: supervise-eval scenario <scenario.json> | score [--input observations.jsonl]")
	}
	switch args[0] {
	case "scenario":
		if len(args) != 2 {
			return errors.New("scenario requires exactly one JSON file")
		}
		scenario, err := superviseeval.LoadScenario(args[1])
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(scenario)
	case "score":
		flags := flag.NewFlagSet("score", flag.ContinueOnError)
		flags.SetOutput(stderr)
		inputPath := "-"
		flags.StringVar(&inputPath, "input", "-", "JSONL observations file ('-' for stdin)")
		flags.StringVar(&inputPath, "i", "-", "JSONL observations file ('-' for stdin)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("score accepts flags only")
		}
		input := stdin
		var file *os.File
		if inputPath != "" && inputPath != "-" {
			var err error
			file, err = os.Open(inputPath)
			if err != nil {
				return err
			}
			defer file.Close()
			input = file
		}
		observations, err := superviseeval.DecodeObservations(input)
		if err != nil {
			return err
		}
		comparisons, err := superviseeval.Compare(observations)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(comparisons); err != nil {
			return fmt.Errorf("encode comparisons: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
