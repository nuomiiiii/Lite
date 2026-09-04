package remote

import (
	"strings"
	"testing"
)

func TestFileOperationAuditOmitsPaths(t *testing.T) {
	mutation := fileOperationAuditDetail([]byte(`{"type":"file.rename","path":"/tmp/old\nname","destination":"/tmp/new"}`))
	if mutation != "operation:file.rename" {
		t.Fatalf("unexpected audit detail: %q", mutation)
	}
	if strings.Contains(mutation, "/tmp") || strings.Contains(mutation, "old") {
		t.Fatalf("audit leaked a path: %q", mutation)
	}
	copyMutation := fileOperationAuditDetail([]byte(`{"type":"file.copy","path":"/tmp/source","destination":"/tmp/destination"}`))
	if copyMutation != "operation:file.copy" {
		t.Fatalf("copy was not audited correctly: %q", copyMutation)
	}
	for _, payload := range []string{
		`{"type":"file.list","path":"/tmp"}`,
		`{"type":"file.download","path":"/tmp/file"}`,
		`{"type":"file.upload.chunk","data":"large"}`,
		`not-json`,
	} {
		if detail := fileOperationAuditDetail([]byte(payload)); detail != "" {
			t.Fatalf("read-only or invalid payload was audited: %q", detail)
		}
	}
}
