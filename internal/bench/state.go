package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const stateChunks = 12

type stateAnswer struct {
	Files    int `json:"files"`
	Values   int `json:"values"`
	Checksum int `json:"checksum"`
}

// persistentStateTask checks whether a model can carry a computed value from
// one run_code script into another without putting it in model context.
func persistentStateTask() Task {
	return Task{
		Name: "persistent-script-state",
		Prompt: "Compute the aggregate described by MANIFEST.md and write it as JSON to STATE_RESULT.json. " +
			"When run_code is available, you must use two separate run_code calls: the first reads every input, computes the object, and calls store(\"benchmark.aggregate\", object) without writing STATE_RESULT.json; after that call ends, a later run_code call must load(\"benchmark.aggregate\") and write that loaded object to STATE_RESULT.json without rereading the chunks. " +
			"Do not combine those stages into one script. When run_code is unavailable, compute and write the result with the ordinary tools. Don't change any input file.",
		Setup: func(dir string) error {
			if err := os.Mkdir(filepath.Join(dir, "chunks"), 0o755); err != nil {
				return err
			}
			manifest := "For chunkNN.txt, multiply every integer in the file by NN, then add every product.\n" +
				"STATE_RESULT.json must be a JSON object with exactly these numeric fields: files is the number of chunk files, values is the total number of integers across them, and checksum is the sum of all weighted products.\n"
			if err := writeFile(dir, "MANIFEST.md", manifest); err != nil {
				return err
			}
			for i := 1; i <= stateChunks; i++ {
				content := fmt.Sprintf("%d\n%d\n%d\n", i+2, i*3-1, i*i+4)
				if err := writeFile(dir, fmt.Sprintf("chunks/chunk%02d.txt", i), content); err != nil {
					return err
				}
			}
			return nil
		},
		Verify: func(dir string) (bool, string) {
			data, err := os.ReadFile(filepath.Join(dir, "STATE_RESULT.json"))
			if err != nil {
				return false, "STATE_RESULT.json is missing"
			}
			var got stateAnswer
			if err := json.Unmarshal(data, &got); err != nil {
				return false, "STATE_RESULT.json is not valid JSON: " + err.Error()
			}
			want := persistentStateAnswer()
			if got != want {
				return false, fmt.Sprintf("got %+v, want %+v", got, want)
			}
			return true, ""
		},
		requireScriptState: true,
	}
}

func persistentStateAnswer() stateAnswer {
	answer := stateAnswer{Files: stateChunks, Values: stateChunks * 3}
	for i := 1; i <= stateChunks; i++ {
		answer.Checksum += i * ((i + 2) + (i*3 - 1) + (i*i + 4))
	}
	return answer
}
