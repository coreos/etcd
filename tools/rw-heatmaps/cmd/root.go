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

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"go.etcd.io/etcd/tools/rw-heatmaps/v3/pkg/chart"
	"go.etcd.io/etcd/tools/rw-heatmaps/v3/pkg/dataset"
)

var (
	// ErrMissingTitleArg is returned when the title argument is missing.
	ErrMissingTitleArg = fmt.Errorf("missing title argument")
	// ErrMissingOutputImageFileArg is returned when the output image file argument is missing.
	ErrMissingOutputImageFileArg = fmt.Errorf("missing output image file argument")
	// ErrMissingInputFileArg is returned when the input file argument is missing.
	ErrMissingInputFileArg = fmt.Errorf("missing input file argument")
	// ErrInvalidOutputFormat is returned when the output format is invalid.
	ErrInvalidOutputFormat = fmt.Errorf("invalid output format, must be one of png, jpg, jpeg, tiff")
	// ErrInvalidChartTYpe is returned when the chart type is invalid.
	ErrInvalidChartType = fmt.Errorf("invalid chart type, must be one of line, heatmap")
)

// NewRootCommand returns the root command for the rw-heatmaps tool.
func NewRootCommand() *cobra.Command {
	o := newOptions()
	rootCmd := &cobra.Command{
		Use:   "rw-heatmaps [input file(s) in csv format]",
		Short: "A tool to generate read/write heatmaps for etcd3",
		Long:  "rw-heatmaps is a tool to generate read/write heatmaps images for etcd3.",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Validate(); err != nil {
				return err
			}

			datasets := make([]*dataset.DataSet, len(args))
			for i, arg := range args {
				var err error
				if datasets[i], err = dataset.LoadCSVData(arg); err != nil {
					return err
				}
			}

			if o.chartType == "line" {
				return chart.PlotLineCharts(datasets, o.title, o.outputImageFile, o.outputFormat)
			}

			return chart.PlotHeatMaps(datasets, o.title, o.outputImageFile, o.outputFormat, o.zeroCentered)
		},
	}

	o.AddFlags(rootCmd.Flags())
	rootCmd.AddCommand(newCompactionChartCmd())
	return rootCmd
}

// options holds the options for the command.
type options struct {
	title           string
	outputImageFile string
	outputFormat    string
	zeroCentered    bool
	chartType       string
}

// newOptions returns a new options for the command with the default values applied.
func newOptions() options {
	return options{
		outputFormat: "jpg",
		zeroCentered: true,
		chartType:    "heatmap",
	}
}

// AddFlags sets the flags for the command.
func (o *options) AddFlags(fs *pflag.FlagSet) {
	fs.StringVarP(&o.title, "title", "t", o.title, "plot graph title (required)")
	fs.StringVarP(&o.outputImageFile, "output-image-file", "o", o.outputImageFile, "output image filename (required)")
	fs.StringVarP(&o.outputFormat, "output-format", "f", o.outputFormat, "output image file format")
	fs.BoolVar(&o.zeroCentered, "zero-centered", o.zeroCentered, "plot the improvement graph with white color represents 0.0")
	fs.StringVarP(&o.chartType, "chart-type", "c", o.chartType, `type of chart to plot ["line", or "heatmap"]`)
}

// Validate returns an error if the options are invalid.
func (o *options) Validate() error {
	if o.title == "" {
		return ErrMissingTitleArg
	}
	if o.outputImageFile == "" {
		return ErrMissingOutputImageFileArg
	}
	switch o.outputFormat {
	case "png", "jpg", "jpeg", "tiff":
	default:
		return ErrInvalidOutputFormat
	}
	switch o.chartType {
	case "line", "heatmap":
	default:
		return ErrInvalidChartType
	}
	return nil
}
