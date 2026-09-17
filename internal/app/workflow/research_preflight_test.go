package workflow

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResearchPreflightChecksWholeCSVAndPreservesQuotedNewlines(t *testing.T) {
	python, err := exec.LookPath("python")
	if err != nil {
		t.Skip("Python unavailable")
	}
	for _, tc := range []struct {
		name, body string
		valid      bool
		rows       int
	}{
		{"quoted newline", "id,note\n1,\"sleep\nmeasurement\"\n2,normal\n", true, 2},
		{"whole file", "id,value\n" + strings.Repeat("1,2\n", 30), true, 30},
		{"late malformed row", "id,value\n" + strings.Repeat("1,2\n", 25) + "1,2,3\n", false, 0},
		{"header only", "id,value\n", false, 0},
		{"empty", "", false, 0},
		{"duplicate columns", "score,score\n1,2\n", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			input := filepath.Join(dir, "data.csv")
			output := filepath.Join(dir, "preflight.json")
			if err := os.WriteFile(input, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			inputs, _ := json.Marshal([]string{input})
			outputs, _ := json.Marshal([]string{output})
			script := "SCIAIDE_INPUTS=" + string(inputs) + "\nSCIAIDE_OUTPUTS=" + string(outputs) + "\n" + dynamicDataPreflightCode
			log, err := exec.Command(python, "-c", script).CombinedOutput()
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v log=%s", tc.valid, err, log)
			}
			if !tc.valid {
				if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatal("failed preflight published output")
				}
				return
			}
			data, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			var value struct {
				Rows    int        `json:"rowCount"`
				Checked bool       `json:"shapeCheckedAllRows"`
				Preview [][]string `json:"preview"`
			}
			if json.Unmarshal(data, &value) != nil || value.Rows != tc.rows || !value.Checked || len(value.Preview) > 10 {
				t.Fatalf("preflight=%s", data)
			}
			if tc.name == "quoted newline" && value.Preview[0][1] != "sleep\nmeasurement" {
				t.Fatal("embedded newline corrupted")
			}
		})
	}
}

func TestResearchPreflightChecksEverySelectedFile(t *testing.T) {
	python, err := exec.LookPath("python")
	if err != nil {
		t.Skip("Python unavailable")
	}
	dir := t.TempDir()
	first := filepath.Join(dir, "first.csv")
	second := filepath.Join(dir, "second.tsv")
	output := filepath.Join(dir, "preflight.json")
	os.WriteFile(first, []byte("id,value\n1,2\n"), 0600)
	for _, valid := range []bool{true, false} {
		body := "id\tgroup\n1\ta\n2\tb\n"
		if !valid {
			body = "id\tgroup\n1\ta\textra\n"
		}
		os.WriteFile(second, []byte(body), 0600)
		inputs, _ := json.Marshal([]string{first, second})
		outputs, _ := json.Marshal([]string{output})
		log, err := exec.Command(python, "-c", "SCIAIDE_INPUTS="+string(inputs)+"\nSCIAIDE_OUTPUTS="+string(outputs)+"\n"+dynamicDataPreflightCode).CombinedOutput()
		if (err == nil) != valid {
			t.Fatalf("%v %s", err, log)
		}
		if valid {
			data, _ := os.ReadFile(output)
			var result struct {
				Files []struct {
					Rows int `json:"rowCount"`
				} `json:"files"`
			}
			if json.Unmarshal(data, &result) != nil || len(result.Files) != 2 || result.Files[1].Rows != 2 {
				t.Fatal(string(data))
			}
		}
	}
}
