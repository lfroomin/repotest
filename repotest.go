package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"gotest.tools/gotestsum/testjson"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type color string

const (
	colorRed    color = "\u001b[31m"
	colorGreen        = "\u001b[32m"
	colorYellow       = "\u001b[33m"
	colorBlue         = "\u001b[34m"
	colorReset        = "\u001b[0m"
)
const iconSuccess = colorGreen + "✓" + colorReset
const iconFailure = colorRed + "✖" + colorReset
const iconSkipped = colorYellow + "∅" + colorReset

func main() {
	defer exeTime()()

	showFailures := flag.Bool("showFail", false, "display failed test details")
	useCache := flag.Bool("useCache", true, "use test cache")
	flag.Parse()

	wPath, paths := getWorkspacePaths()
	fmt.Printf("Using %s/go.work file\n", wPath)

	results := execAllTests(wPath, paths, *useCache)

	printResults(results, *showFailures)
}

// exeTime is used to compute and display the total execution time for all tests
func exeTime() func() {
	start := time.Now()
	return func() {
		fmt.Printf("%sTest execution time: %s%s\n", colorBlue, testjson.FormatDurationAsSeconds(time.Since(start), 3), colorReset)
	}
}

// getWorkspacePaths locates the go.work file and returns the list of packages
// contained within. The workspace file is located by starting at the current
// directory and traversing towards the root directory until a workspace file
// is found.
func getWorkspacePaths() (string, []string) {
	const workFile = "go.work"

	path, err := os.Getwd()
	if err != nil {
		log.Println(err)
		return "", nil
	}

	done := false
	for !done {
		entries, err := os.ReadDir(path)
		if err != nil {
			log.Println(err)
			return "", nil
		}

		for _, e := range entries {
			if e.Name() == workFile {
				// The workspace file has been found
				return path, readWorkspaceFile(path, e.Name())
			}
		}

		// The workspace file was not found in this directory, so set the
		// path to the parent directory for the next loop iteration.
		path = filepath.Dir(path)

		// Check if new path is the root directory
		if len(path) == 1 {
			done = true
		}
	}

	// The workspace file was not found
	return "", nil
}

// readWorkspaceFile reads the go.work file and returns the list of packages contained
// within the "use( ... )" syntax. The package locations found in the workspace file
// are concatenated with the input path to create file names that include a full path
func readWorkspaceFile(path, filename string) []string {
	fullFilename := filepath.Join(path, filename)
	f, err := os.Open(fullFilename)
	if err != nil {
		log.Println(err)
		return nil
	}
	defer func(f *os.File) {
		err := f.Close()
		if err != nil {
			log.Println(err)
		}
	}(f)

	var packages []string
	scanner := bufio.NewScanner(f)
	beginCapture := false
	for scanner.Scan() {
		line := scanner.Text()
		if beginCapture && !strings.Contains(line, ")") {
			dirName := filepath.Join(path, strings.TrimSpace(line))
			packages = append(packages, dirName)
		} else if strings.HasPrefix(line, "use (") {
			beginCapture = true
		}
	}

	if err = scanner.Err(); err != nil {
		log.Println(err)
		return nil
	}

	return packages
}

// execAllTests executes tests for all packages found in the workspace file, scans
// the output of the test, and compiles the results. All tests in the package path
// and in the directories below are executed (./...).
func execAllTests(wPath string, paths []string, useCache bool) []testAnalysis {
	if paths == nil || len(paths) == 0 {
		return nil
	}

	results := make([]testAnalysis, 0, len(paths))
	resultCh := make(chan testAnalysis, len(paths))

	go func() {
		var wg sync.WaitGroup
		for _, p := range paths {
			wg.Add(1)
			go func(p string) {
				defer wg.Done()
				out := execTest(p, useCache)
				resultCh <- analyzeTest(wPath, p, out)
			}(p)
		}
		wg.Wait()
		close(resultCh)
	}()

	for r := range resultCh {
		results = append(results, r)
	}

	return results
}

// execTest executes all tests for a given path and the directories below. The output
// of the tests is captured for later analysis.
func execTest(dirPath string, useCache bool) []byte {
	testDir := filepath.Join(dirPath, "...")

	var output []byte
	if useCache {
		output, _ = exec.Command("go", "test", "-json", testDir).Output()
	} else {
		output, _ = exec.Command("go", "test", "-count=1", "-json", testDir).Output()
	}

	return output
}

// analyzeTest uses the testjson package to analyze the results of the test.
func analyzeTest(wPath, path string, output []byte) testAnalysis {
	testExec, err := testjson.ScanTestOutput(testjson.ScanConfig{
		Stdout: bytes.NewReader(output),
	})
	if err != nil {
		log.Panic(fmt.Errorf("failed to scan testjson: %w", err))
	}

	return testAnalysis{
		label:    removeRelativePath(wPath, path),
		testExec: testExec,
	}
}

// removeRelativePath strips the workspace path prefix from the supplied path.
// This provides a shorter, less repetitive label for the test results.
func removeRelativePath(wPath, path string) string {
	shortPath, _ := filepath.Rel(wPath, path)
	return shortPath
}

// printResults outputs the results from all the tests. If displayFailures is enabled,
// then tests that have failed will output with additional details about how the test
// failed.
func printResults(results []testAnalysis, displayFailures bool) {
	if results == nil || len(results) == 0 {
		return
	}

	// Sort results
	sort.Slice(results, func(i, j int) bool {
		return results[i].label < results[j].label
	})

	labelLen := getMaxLabel(results)

	fmt.Println("\nResults:")
	for _, ta := range results {
		ta.printSummary(labelLen)
	}
	fmt.Println()

	if displayFailures {
		for _, ta := range results {
			ta.printFailedDetail()
		}
		fmt.Println()
	}
}

// getMaxLabel computes the max label length which is used for formatting the output
func getMaxLabel(results []testAnalysis) int {
	maxLen := 0

	for _, ta := range results {
		if len(ta.label) > maxLen {
			maxLen = len(ta.label)
		}
	}

	// Increment by 1 to pad the label column
	maxLen++

	return maxLen
}

type testAnalysis struct {
	testExec *testjson.Execution
	label    string
}

// printSummary prints the test results in a single summary line with the appropriate icon
func (ta testAnalysis) printSummary(labelLen int) {
	if ta.testExec.Total() == 0 {
		fmt.Printf("%s %*s\n", iconSkipped, labelLen, ta.label)
		return
	}

	var icon color = iconSuccess
	if len(ta.testExec.Errors()) > 0 || len(ta.testExec.Failed()) > 0 {
		icon = iconFailure
		fmt.Printf("%s %*s  %3d tests (%s) (%d errors) (%d failed)\n", icon, labelLen, ta.label, ta.testExec.Total(), testjson.FormatDurationAsSeconds(ta.testExec.Elapsed(), 3), len(ta.testExec.Errors()), len(ta.testExec.Failed()))
		return
	}

	fmt.Printf("%s %*s  %3d tests (%s)\n", icon, labelLen, ta.label, ta.testExec.Total(), testjson.FormatDurationAsSeconds(ta.testExec.Elapsed(), 3))
}

// printFailedDetail prints the detailed test results for failed tests
func (ta testAnalysis) printFailedDetail() {
	if len(ta.testExec.Errors()) > 0 || len(ta.testExec.Failed()) > 0 {
		cnt := (80 - len(ta.label)) / 2
		eq := strings.Repeat("=", cnt)
		fmt.Printf("\n%s%s  %s  %s%s\n", colorRed, eq, ta.label, eq, colorReset)
		testjson.PrintSummary(os.Stdout, ta.testExec, testjson.SummarizeAll)
	}
}
