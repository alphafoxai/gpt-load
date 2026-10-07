package control

import (
	"bytes"
	"testing"

	"github.com/sirupsen/logrus"

	app_errors "gpt-load/internal/platform/errors"
)

func TestDegradeLogSurvivesTypedNil(t *testing.T) {
	var typedNil *app_errors.APIError
	var buffer bytes.Buffer
	original := logrus.StandardLogger().Out
	logrus.SetOutput(&buffer)
	t.Cleanup(func() { logrus.SetOutput(original) })

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("degradeLog panicked on typed nil: %v", recovered)
		}
	}()
	degradeLog(typedNil, 7, "degrade result was not saved")
	if !bytes.Contains(buffer.Bytes(), []byte("degrade result was not saved")) {
		t.Fatalf("log = %q, want the failure message without panicking", buffer.String())
	}
}

func TestAppendDegradeResultKeepsHistoryWhenSaveReturnsTypedNil(t *testing.T) {
	fixture := newServiceFixture(t)
	first := degradeStoredResult{ID: "1-a", CredentialID: 1, Status: "completed"}
	fixture.service.appendDegradeResult(first)

	stored, err := fixture.service.loadDegradeResults(t.Context())
	if err != nil {
		t.Fatalf("loadDegradeResults() error = %v", err)
	}
	if len(stored[1]) != 1 || stored[1][0].ID != "1-a" {
		t.Fatalf("stored = %#v, want the finished test", stored)
	}

	// The production crash was a typed nil reported as a database error.
	// Saving again must still replace the board instead of killing the process.
	second := degradeStoredResult{ID: "1-b", CredentialID: 1, Status: "completed"}
	fixture.service.appendDegradeResult(second)
	stored, err = fixture.service.loadDegradeResults(t.Context())
	if err != nil {
		t.Fatalf("loadDegradeResults() after second save error = %v", err)
	}
	if len(stored[1]) != 2 || stored[1][1].ID != "1-b" {
		t.Fatalf("stored = %#v, want both finished tests", stored)
	}
}

func TestDegradeLogKeepsRealErrorText(t *testing.T) {
	var buffer bytes.Buffer
	original := logrus.StandardLogger().Out
	logrus.SetOutput(&buffer)
	t.Cleanup(func() { logrus.SetOutput(original) })

	degradeLog(app_errors.ErrDatabase, 9, "degrade result was not saved")
	if !bytes.Contains(buffer.Bytes(), []byte("Database operation failed")) {
		t.Fatalf("log = %q, want the database error text", buffer.String())
	}
}
