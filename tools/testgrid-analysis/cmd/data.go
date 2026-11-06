// Copyright 2024 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	apipb "github.com/GoogleCloudPlatform/testgrid/pb/api/v1"
	statuspb "github.com/GoogleCloudPlatform/testgrid/pb/test_status"
	"google.golang.org/protobuf/encoding/protojson"
)

const testGridAPIBaseURL = "https://testgrid-api.prow.k8s.io"

var (
	validTestStatuses      = []statuspb.TestStatus{statuspb.TestStatus_PASS, statuspb.TestStatus_FAIL, statuspb.TestStatus_FLAKY}
	failureTestStatuses    = []statuspb.TestStatus{statuspb.TestStatus_FAIL, statuspb.TestStatus_FLAKY}
	validTestStatusesInt   = intStatusSet(validTestStatuses)
	failureTestStatusesInt = intStatusSet(failureTestStatuses)

	skippedTestStatuses = make(map[int32]struct{})
)

type TestResultSummary struct {
	Name                  string
	FullName              string
	TotalRuns, FailedRuns int
	FailureRate           float32
	FailureLogs           []string
	IssueBody             string
}

func fetchTestResultSummaries(dashboard, tab string) []*TestResultSummary {
	// Fetch test data
	rowsURL := fmt.Sprintf("%s/api/v1/dashboards/%s/tabs/%s/rows", testGridAPIBaseURL, dashboard, tab)
	headersURL := fmt.Sprintf("%s/api/v1/dashboards/%s/tabs/%s/headers", testGridAPIBaseURL, dashboard, tab)

	var testData apipb.ListRowsResponse
	var headerData apipb.ListHeadersResponse
	protojson.Unmarshal(fetchJSON(rowsURL), &testData)
	protojson.Unmarshal(fetchJSON(headersURL), &headerData)

	var allTests []string
	for _, row := range testData.Rows {
		allTests = append(allTests, row.Name)
	}

	summaries := []*TestResultSummary{}
	// Process rows
	for _, row := range testData.Rows {
		t := processRow(dashboard, tab, row, allTests, headerData.Headers)
		summaries = append(summaries, t)
	}
	return summaries
}

func processRow(dashboard, tab string, row *apipb.ListRowsResponse_Row, allTests []string, headers []*apipb.ListHeadersResponse_Header) *TestResultSummary {
	t := TestResultSummary{Name: shortenTestName(row.Name), FullName: row.Name}
	// we do not want to create issues for a parent test.
	if isParentTest(row.Name, allTests) {
		return &t
	}
	if !strings.HasPrefix(row.Name, "go.etcd.io") {
		return &t
	}
	earliestTimeToConsider := time.Now().AddDate(0, 0, -1*maxDays)
	total := 0
	failed := 0
	logs := []string{}
	for i, cell := range row.Cells {
		// ignore tests with status not in the validTestStatuses
		// cell result codes are listed in https://github.com/GoogleCloudPlatform/testgrid/blob/main/pb/test_status/test_status.proto
		if _, ok := validTestStatusesInt[cell.Result]; !ok {
			if cell.Result != 0 {
				skippedTestStatuses[cell.Result] = struct{}{}
			}
			continue
		}
		header := headers[i]
		if maxDays > 0 && header.Started.AsTime().Before(earliestTimeToConsider) {
			continue
		}
		total++
		if _, ok := failureTestStatusesInt[cell.Result]; ok {
			failed++
			// markdown table format of | commit | log |
			logs = append(logs, fmt.Sprintf("| %s | %s | https://prow.k8s.io/view/gs/kubernetes-jenkins/logs/%s/%s |", strings.Join(header.Extra, ","), header.Started.AsTime().String(), tab, header.Build))
		}
		if maxRuns > 0 && total >= maxRuns {
			break
		}
	}
	t.FailedRuns = failed
	t.TotalRuns = total
	t.FailureLogs = logs
	t.FailureRate = float32(failed) / float32(total)
	if t.FailedRuns > 0 {
		dashboardURL := fmt.Sprintf("[%s](https://testgrid.k8s.io/%s#%s)", tab, dashboard, tab)
		t.IssueBody = fmt.Sprintf("## %s Test: %s \nTest failed %.1f%% (%d/%d) of the time\n\nfailure logs are:\n| commit | started | log |\n| --- | --- | --- |\n%s\n",
			dashboardURL, t.FullName, t.FailureRate*100, t.FailedRuns, t.TotalRuns, strings.Join(t.FailureLogs, "\n"))
		t.IssueBody += "\nPlease follow the [instructions in the contributing guide](https://github.com/etcd-io/etcd/blob/main/CONTRIBUTING.md#check-for-flaky-tests) to reproduce the issue.\n"
		fmt.Printf("%s failed %.1f%% (%d/%d) of the time\n", t.FullName, t.FailureRate*100, t.FailedRuns, t.TotalRuns)
	}
	return &t
}

// isParentTest checks if a test is a rollup of some child tests.
func isParentTest(test string, allTests []string) bool {
	for _, t := range allTests {
		if t != test && strings.HasPrefix(t, test+"/") {
			return true
		}
	}
	return false
}

func fetchJSON(url string) []byte {
	resp, err := http.Get(url)
	if err != nil {
		fmt.Println("Error fetching test data:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	testBody, _ := io.ReadAll(resp.Body)
	return testBody
}

// intStatusSet converts a list of statuspb.TestStatus into a set of int.
func intStatusSet(statuses []statuspb.TestStatus) map[int32]struct{} {
	s := make(map[int32]struct{})
	for _, status := range statuses {
		s[int32(status)] = struct{}{}
	}
	return s
}

func shortenTestName(fullname string) string {
	parts := strings.Split(fullname, ".")
	return parts[len(parts)-1]
}
