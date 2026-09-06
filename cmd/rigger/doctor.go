package main

import (
	"fmt"
	"os"

	"github.com/sanjaynagpal/stage/internal/doctor"
	"github.com/sanjaynagpal/stage/internal/layout"
	"github.com/sanjaynagpal/stage/internal/wizard"
)

// runDoctor implements rigger.exe --doctor (docs/REQUIREMENTS.md §19-20,
// §24): runs every diagnostic check, best-effort collects logs, and shows
// the result in a local browser page. Reuses internal/wizard rather than
// internal/tui specifically so this rarely-used mode doesn't pull
// bubbletea/lipgloss into rigger.exe's every-launch binary — internal/wizard
// is stdlib-only.
func runDoctor() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("rigger: could not determine my own location: %w", err)
	}
	appID := layout.AppIDFromExePath(exePath)

	report := doctor.Run(appID)

	logZipPath := ""
	if report.DataDir != "" {
		if zipPath, err := doctor.CollectLogs(report.DataDir); err == nil {
			logZipPath = zipPath
		} else {
			fmt.Fprintf(os.Stderr, "rigger: warning: could not collect logs: %v\n", err)
		}
	}

	checks := make([]wizard.CheckResult, len(report.Checks))
	for i, c := range report.Checks {
		checks[i] = wizard.CheckResult{Name: c.Name, Status: c.Status.String(), Detail: c.Detail}
	}

	return wizard.ShowDoctorReport(wizard.DoctorReport{
		AppID:        appID,
		Checks:       checks,
		LogZipPath:   logZipPath,
		SupportEmail: report.SupportEmail,
	})
}
