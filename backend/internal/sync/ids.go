// Package sync implements the generic synchronization engine.
package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// documentNamespace roots deterministic destination document IDs.
var documentNamespace = uuid.MustParse("6f1c9a52-4b7e-4f0e-9a43-1b2d7c3e5f80")

// DocumentID derives the deterministic destination document ID from
// task_id + source_item_id. It never depends on names or paths, so renames,
// moves, retries and duplicate executions converge on the same document.
func DocumentID(taskID, sourceItemID string) string {
	return "hi_" + uuid.NewSHA1(documentNamespace, []byte(taskID+"\x00"+sourceItemID)).String()
}

// OperationID derives an idempotency key for a retain submission.
func OperationID(runID string, parts []string) string {
	sorted := append([]string(nil), parts...)
	sort.Strings(sorted)
	return uuid.NewSHA1(documentNamespace, []byte(runID+"\x02"+strings.Join(sorted, "\x01"))).String()
}

// Fingerprint captures everything that determines the destination document:
// source revision plus the retain policy (strategy, tags, metadata, revision).
func Fingerprint(revision string, policyRevision int64, strategy string, tags []string, metadata map[string]string) string {
	payload, _ := json.Marshal(struct {
		R string            `json:"r"`
		P int64             `json:"p"`
		S string            `json:"s"`
		T []string          `json:"t"`
		M map[string]string `json:"m"`
	}{revision, policyRevision, strategy, tags, metadata})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:16])
}
