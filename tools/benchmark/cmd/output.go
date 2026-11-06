// Copyright 2026 The etcd Authors
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
	"os"

	"go.etcd.io/etcd/pkg/v3/report"
)

var outputFormat string

func init() {
	RootCmd.PersistentFlags().StringVar(&outputFormat, "output-format", "text",
		`Output format for benchmark results. One of: "text" (default) or "json".`)
}

// printReport writes benchmark statistics to stdout in the format chosen by
// --output-format. All sub-commands should call this function instead of
// printing report.Run() output directly so that JSON support is automatic.
// label, if non-empty, identifies the report -- it is printed as a heading
// in text mode and included as the "label" field in JSON mode. Use it when
// a sub-command prints more than one report (e.g. watch.go); pass "" when
// a sub-command only prints a single report.
func printReport(r report.Report, label string) func() {
	switch outputFormat {
	case "json":
		rc := r.Stats()
		return func() {
			s := <-rc
			out, err := report.FormatJSON(s, label)
			if err != nil {
				fmt.Fprintf(os.Stderr, "benchmark: JSON encode error: %v\n", err)
				os.Exit(1)
			}
			fmt.Println(out)
		}
	default:
		rc := r.Run()
		return func() {
			if label != "" {
				fmt.Printf("%s:\n%s", label, <-rc)
			} else {
				fmt.Print(<-rc)
			}
		}
	}
}
