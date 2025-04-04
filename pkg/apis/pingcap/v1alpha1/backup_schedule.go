// Copyright 2019 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package v1alpha1

import (
	"fmt"
	"time"
)

func (bs *BackupSchedule) GetBackupCRDName(timestamp time.Time) string {
	return fmt.Sprintf("%s-%s", bs.GetName(), timestamp.UTC().Format(BackupNameTimeFormat))
}

func (bs *BackupSchedule) GetLogBackupCRDName() string {
	return fmt.Sprintf("%s-%s", "log", bs.GetName())
}

func (bs *BackupSchedule) GetCompactBackupCRDName(timestamp time.Time) string {
	return fmt.Sprintf("%s-%s-%s", "compact", bs.GetName(), timestamp.UTC().Format(BackupNameTimeFormat))
}
