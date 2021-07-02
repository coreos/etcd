// Copyright 2020 The etcd Authors
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

package membership

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/coreos/go-semver/semver"
	"go.etcd.io/etcd/api/v3/version"
	"go.uber.org/zap"
)

func TestMustDetectDowngrade(t *testing.T) {
	lv := semver.Must(semver.NewVersion(version.Version))
	lv = &semver.Version{Major: lv.Major, Minor: lv.Minor}
	oneMinorHigher := &semver.Version{Major: lv.Major, Minor: lv.Minor + 1}
	oneMinorLower := &semver.Version{Major: lv.Major, Minor: lv.Minor - 1}
	downgradeEnabledHigherVersion := &DowngradeInfo{Enabled: true, TargetVersion: oneMinorHigher.String()}
	downgradeEnabledEqualVersion := &DowngradeInfo{Enabled: true, TargetVersion: lv.String()}
	downgradeEnabledLowerVersion := &DowngradeInfo{Enabled: true, TargetVersion: oneMinorLower.String()}
	downgradeDisabled := &DowngradeInfo{Enabled: false}

	tests := []struct {
		name                 string
		clusterVersion       *semver.Version
		downgrade            *DowngradeInfo
		unsafeAllowDowngrade bool
		success              bool
		message              string
	}{
		{
			"Succeeded when downgrade is disabled and cluster version is nil",
			nil,
			downgradeDisabled,
			false,
			true,
			"",
		},
		{
			"Succeeded when downgrade is disabled and cluster version is one minor lower",
			oneMinorLower,
			downgradeDisabled,
			false,
			true,
			"",
		},
		{
			"Succeeded when downgrade is disabled and cluster version is server version",
			lv,
			downgradeDisabled,
			false,
			true,
			"",
		},
		{
			"Succeed when downgrade is disabled, unsafeDowngrade is enabled and cluster version is one minor higher",
			oneMinorHigher,
			downgradeDisabled,
			true,
			true,
			"allowing unsafe downgrade; local server version is lower than determined cluster version",
		},
		{
			"Failed when downgrade is disabled and server version is lower than determined cluster version ",
			oneMinorHigher,
			downgradeDisabled,
			false,
			false,
			"invalid downgrade, not allowed; local server version is lower than determined cluster version",
		},
		{
			"Succeeded when downgrade is enabled and cluster version is nil",
			nil,
			downgradeEnabledEqualVersion,
			false,
			true,
			"",
		},
		{
			"Failed when downgrade is enabled and server version is target version",
			lv,
			downgradeEnabledEqualVersion,
			false,
			true,
			"cluster is downgrading to target version",
		},
		{
			"Succeeded when downgrade to lower version and server version is cluster version ",
			lv,
			downgradeEnabledLowerVersion,
			false,
			false,
			"invalid downgrade; server version is not allowed to join when downgrade is enabled",
		},
		{
			"Failed when downgrade is enabled and local version is out of range and cluster version is nil",
			nil,
			downgradeEnabledHigherVersion,
			false,
			false,
			"invalid downgrade; server version is not allowed to join when downgrade is enabled",
		},
		{
			"Failed when downgrade is enabled and local version is out of range",
			lv,
			downgradeEnabledHigherVersion,
			false,
			false,
			"invalid downgrade; server version is not allowed to join when downgrade is enabled",
		},
	}

	if os.Getenv("DETECT_DOWNGRADE") != "" {
		i := os.Getenv("DETECT_DOWNGRADE")
		iint, _ := strconv.Atoi(i)
		logPath := filepath.Join(os.TempDir(), fmt.Sprintf("test-log-must-detect-downgrade-%v", iint))

		lcfg := zap.NewProductionConfig()
		lcfg.OutputPaths = []string{logPath}
		lcfg.ErrorOutputPaths = []string{logPath}
		lg, _ := lcfg.Build()

		mustDetectDowngrade(lg, tests[iint].clusterVersion, tests[iint].downgrade, tests[iint].unsafeAllowDowngrade)
		return
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logPath := filepath.Join(os.TempDir(), fmt.Sprintf("test-log-must-detect-downgrade-%d", i))
			t.Log(logPath)
			defer os.RemoveAll(logPath)

			cmd := exec.Command(os.Args[0], "-test.run=TestMustDetectDowngrade")
			cmd.Env = append(os.Environ(), fmt.Sprintf("DETECT_DOWNGRADE=%d", i))
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}

			errCmd := cmd.Wait()

			data, err := ioutil.ReadFile(logPath)
			if err == nil {
				if !bytes.Contains(data, []byte(tt.message)) {
					t.Errorf("Expected to find %v in log", tt.message)
				}
			} else {
				t.Fatal(err)
			}

			if !tt.success {
				e, ok := errCmd.(*exec.ExitError)
				if !ok || e.Success() {
					t.Errorf("Expected exit with status 1; Got %v", err)
				}
			}

			if tt.success && errCmd != nil {
				t.Errorf("Expected not failure; Got %v", errCmd)
			}
		})
	}
}

func TestIsValidDowngrade(t *testing.T) {
	tests := []struct {
		name    string
		verFrom string
		verTo   string
		result  bool
	}{
		{
			"Valid downgrade",
			"3.5.0",
			"3.4.0",
			true,
		},
		{
			"Invalid downgrade",
			"3.5.2",
			"3.3.0",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := isValidDowngrade(
				semver.Must(semver.NewVersion(tt.verFrom)), semver.Must(semver.NewVersion(tt.verTo)))
			if res != tt.result {
				t.Errorf("Expected downgrade valid is %v; Got %v", tt.result, res)
			}
		})
	}
}
