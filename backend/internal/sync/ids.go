// Package sync implements the generic synchronization engine.
package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// operationNamespace roots retain idempotency keys.
var operationNamespace = uuid.MustParse("6f1c9a52-4b7e-4f0e-9a43-1b2d7c3e5f80")

// DocumentID isolates source items by task while remaining stable across runs.
func DocumentID(sourceType, taskID, itemID string) string {
	// The byte length separates the IDs without escaping or normalizing them.
	identity := strconv.Itoa(len(taskID)) + ":" + taskID + itemID
	sum := sha256.Sum256([]byte(identity))
	return sourceType + ":" + hex.EncodeToString(sum[:])
}

// OperationID derives an idempotency key for a retain submission.
func OperationID(runID string, parts []string) string {
	sorted := append([]string(nil), parts...)
	sort.Strings(sorted)
	return uuid.NewSHA1(operationNamespace, []byte(runID+"\x02"+strings.Join(sorted, "\x01"))).String()
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
