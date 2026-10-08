// Package sync implements the generic synchronization engine.
package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

// operationNamespace roots retain idempotency keys.
var operationNamespace = uuid.MustParse("6f1c9a52-4b7e-4f0e-9a43-1b2d7c3e5f80")

// CanonicalIdentity returns the source-level identity used as Hindsight's
// document_id. It deliberately excludes the ingestion task ID.
func CanonicalIdentity(sourceType string, cred connectors.Credential, config map[string]any, item connectors.SourceItem) string {
	switch sourceType {
	case "notion":
		return "notion_page:" + item.ID
	case "siyuan":
		instanceID := cred.String("instanceId")
		if instanceID == "" {
			instanceID = cred.String("baseUrl")
		}
		return "siyuan:" + identityURL(instanceID) + ":" + item.ID
	case "s3":
		endpoint := identityURL(cred.String("endpoint"))
		if endpoint == "" {
			endpoint = "s3.amazonaws.com"
		}
		return "s3:" + endpoint + ":" + connectors.AsString(config["bucket"]) + ":" + item.Path
	case "webdav":
		return "webdav:" + identityURL(cred.String("baseUrl")) + ":" + item.Path
	case "google_drive":
		driveID := item.Metadata["google_drive_id"]
		if driveID == "" {
			driveID = connectors.AsString(config["driveId"])
		}
		return "google_drive:" + driveID + ":" + item.ID
	case "onedrive":
		driveID := item.Metadata["onedrive_drive_id"]
		itemID := item.Metadata["onedrive_item_id"]
		if driveID == "" || itemID == "" {
			parts := strings.SplitN(item.ID, "!", 2)
			if len(parts) == 2 {
				driveID, itemID = parts[0], parts[1]
			}
		}
		return "onedrive:" + driveID + ":" + itemID
	case "filesystem":
		fullPath := filepath.Join(cred.String("rootPath"), filepath.FromSlash(item.ID))
		if abs, err := filepath.Abs(fullPath); err == nil {
			fullPath = abs
		}
		return "filesystem:" + filepath.Clean(fullPath)
	default:
		// Preserve a deterministic identity for third-party connectors.
		return item.ID
	}
}

func identityURL(value string) string {
	value = strings.TrimSpace(value)
	for _, scheme := range []string{"https://", "http://"} {
		if strings.HasPrefix(strings.ToLower(value), scheme) {
			value = value[len(scheme):]
			break
		}
	}
	return strings.TrimRight(value, "/")
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
