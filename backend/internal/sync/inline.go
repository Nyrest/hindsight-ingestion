package sync

import (
	"fmt"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
)

func (r *runner) includesImages() bool {
	return r.src.Info().Capabilities.SupportsInlineMultimodal && r.inlineEnabled && r.policy.Images
}

func (r *runner) contentRequest(item connectors.SourceItem) connectors.ContentRequest {
	return connectors.ContentRequest{Credential: r.cred, Config: r.srcCfg, Item: item,
		InlineMultimodalEnabled: r.inlineEnabled, FilePolicy: r.policy, MaxFileSize: r.maxSz}
}

func (r *runner) itemFingerprint(item connectors.SourceItem, tags []string, metadata map[string]string) string {
	revision := item.Revision
	if r.src.Info().Capabilities.SupportsInlineMultimodal {
		revision += fmt.Sprintf("\x00inline-v1:%t:%t:%d", r.inlineEnabled, r.policy.Images, r.maxSz)
	}
	return Fingerprint(revision, r.task.PolicyRevision, r.task.RetainStrategy, tags, metadata)
}
