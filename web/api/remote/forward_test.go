package remote

import "testing"

func TestFileOperationAuditOnlyRecordsMutations(t *testing.T) {
	mutation := fileOperationAuditDetail([]byte(`{"type":"file.rename","path":"/tmp/old\nname","destination":"/tmp/new"}`))
	if mutation == "" {
		t.Fatal("file mutation was not audited")
	}
	if mutation != "operation:file.rename, path:/tmp/old name, destination:/tmp/new" {
		t.Fatalf("unexpected sanitized audit detail: %q", mutation)
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
