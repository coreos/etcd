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
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/vg"
)

// compactionReport is the subset of the benchmark JSON the chart
// renderer needs. Field names match the on-disk JSON.
type compactionReport struct {
	Phases []struct {
		Name    string `json:"Name"`
		StartNs int64  `json:"StartNs"`
		EndNs   int64  `json:"EndNs"`
	} `json:"phases"`
	PerSecond []struct {
		Second           int64   `json:"second"`
		PutP99Ms         float64 `json:"put_p99_ms"`
		RangeP99Ms       float64 `json:"range_p99_ms"`
		CompactionActive bool    `json:"compaction_active"`
	} `json:"per_second"`
	CompactionEvents []struct {
		StartNs int64 `json:"StartNs"`
		EndNs   int64 `json:"EndNs"`
	} `json:"compaction_events"`
}

func newCompactionChartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "compaction-chart <report.json>",
		Short: "Render a P99-over-time chart from a compaction RW impact JSON report",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return renderCompactionChart(args[0])
		},
	}
}

// renderCompactionChart reads the report, draws the chart, and writes
// it next to the input file.
func renderCompactionChart(inPath string) error {
	data, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("read input: %w", err)
	}
	var rep compactionReport
	if err = json.Unmarshal(data, &rep); err != nil {
		return fmt.Errorf("parse input: %w", err)
	}
	if len(rep.PerSecond) == 0 {
		return fmt.Errorf("input has no per_second data")
	}

	// X axis: seconds since the first sample
	t0 := rep.PerSecond[0].Second
	putPts := make(plotter.XYs, 0, len(rep.PerSecond))
	rngPts := make(plotter.XYs, 0, len(rep.PerSecond))
	for _, ps := range rep.PerSecond {
		x := float64(ps.Second - t0)
		putPts = append(putPts, plotter.XY{X: x, Y: ps.PutP99Ms})
		rngPts = append(rngPts, plotter.XY{X: x, Y: ps.RangeP99Ms})
	}

	p := plot.New()
	p.Title.Text = "Compaction R/W Impact — P99 latency over time"
	p.X.Label.Text = "Seconds since start"
	p.Y.Label.Text = "P99 (ms)"
	p.Legend.Top = true
	p.Add(plotter.NewGrid())

	putLine, err := plotter.NewLine(putPts)
	if err != nil {
		return err
	}
	putLine.Color = color.RGBA{R: 0xc0, G: 0x39, B: 0x2b, A: 0xff}
	putLine.Width = vg.Points(1.5)
	p.Add(putLine)
	p.Legend.Add("put_p99", putLine)

	rngLine, err := plotter.NewLine(rngPts)
	if err != nil {
		return err
	}
	rngLine.Color = color.RGBA{R: 0x2b, G: 0x6c, B: 0xc0, A: 0xff}
	rngLine.Width = vg.Points(1.5)
	p.Add(rngLine)
	p.Legend.Add("range_p99", rngLine)

	// Shade compaction windows with a translucent polygon
	shadeColor := color.RGBA{R: 0xff, G: 0xc0, B: 0x00, A: 0x60}
	for _, ev := range rep.CompactionEvents {
		x0 := float64(ev.StartNs/int64(time.Second) - t0)
		x1 := float64(ev.EndNs/int64(time.Second) - t0)
		if x1 < x0 {
			x1 = x0 + 0.5
		}
		poly, err := plotter.NewPolygon(plotter.XYs{
			{X: x0, Y: p.Y.Min},
			{X: x1, Y: p.Y.Min},
			{X: x1, Y: p.Y.Max},
			{X: x0, Y: p.Y.Max},
		})
		if err != nil {
			continue
		}
		poly.Color = shadeColor
		poly.LineStyle.Color = color.RGBA{}
		p.Add(poly)
	}

	outPath := inPath + ".chart.png"
	if err := p.Save(12*vg.Inch, 5*vg.Inch, outPath); err != nil {
		return fmt.Errorf("save chart: %w", err)
	}
	abs, _ := filepath.Abs(outPath)
	fmt.Fprintf(os.Stderr, "wrote chart to %s\n", abs)
	return nil
}
