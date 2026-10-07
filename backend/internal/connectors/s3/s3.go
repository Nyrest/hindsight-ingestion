// Package s3 implements the S3 / S3-compatible source connector.
package s3

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
)

func init() {
	connectors.RegisterCredentialType(connectors.CredentialType{
		Type:        "s3",
		Name:        "S3 / S3-compatible",
		Description: "Amazon S3, MinIO, Cloudflare R2, Backblaze B2, Wasabi, …",
		Fields: []connectors.FieldSpec{
			{Key: "endpoint", Label: "Endpoint", Type: connectors.FieldURL, Placeholder: "https://s3.amazonaws.com",
				Help: "Leave empty for AWS S3."},
			{Key: "region", Label: "Region", Type: connectors.FieldString, Placeholder: "us-east-1"},
			{Key: "accessKeyId", Label: "Access key ID", Type: connectors.FieldString, Required: true},
			{Key: "secretAccessKey", Label: "Secret access key", Type: connectors.FieldPassword, Secret: true, Required: true},
			{Key: "sessionToken", Label: "Session token", Type: connectors.FieldPassword, Secret: true},
			{Key: "pathStyle", Label: "Path-style addressing", Type: connectors.FieldBoolean, Default: false,
				Help: "Required by most self-hosted S3 servers (e.g. MinIO)."},
		},
	})
	connectors.RegisterSource(&Connector{})
}

// Connector implements S3 as a source.
type Connector struct{}

// Info describes the connector.
func (c *Connector) Info() connectors.SourceInfo {
	return connectors.SourceInfo{
		Type:           "s3",
		Name:           "S3",
		CredentialType: "s3",
		Capabilities: connectors.Capabilities{
			IncrementalMode: connectors.IncrementalInventory,
			DeletionMode:    connectors.DeletionScanGeneration,
			SupportsFiles:   true,
		},
		BrowseKinds: []string{connectors.KindBucket, connectors.KindFolder},
		Fields: []connectors.FieldSpec{
			{Key: "bucket", Label: "Bucket", Type: connectors.FieldString, Required: true, Browse: true, BrowseKind: connectors.KindBucket,
				Effect: connectors.EffectRebaseline},
			{Key: "prefix", Label: "Prefix", Type: connectors.FieldString, Placeholder: "documents/", Browse: true, BrowseKind: connectors.KindFolder,
				Help: "Only objects under this key prefix.", Effect: connectors.EffectRebaseline},
			{Key: "recursive", Label: "Include sub-folders", Type: connectors.FieldBoolean, Default: true, Effect: connectors.EffectReconcile},
		},
		FilterFields: fileFilterFields(),
	}
}

func fileFilterFields() []connectors.FilterFieldSpec {
	return []connectors.FilterFieldSpec{
		{Key: "name", Label: "File name", Type: connectors.FilterString, Operators: connectors.StringOps},
		{Key: "path", Label: "Path", Type: connectors.FilterString, Operators: connectors.StringOps},
		{Key: "extension", Label: "Extension", Type: connectors.FilterString, Operators: connectors.StringOps},
		{Key: "fileType", Label: "File type", Type: connectors.FilterEnum, Operators: connectors.EnumOps, Options: []connectors.Option{
			{Value: connectors.GroupPlainText, Label: "Plain text"}, {Value: connectors.GroupDocuments, Label: "Documents"},
			{Value: connectors.GroupImages, Label: "Images"}, {Value: connectors.GroupAudios, Label: "Audios"},
		}},
		{Key: "size", Label: "Size (bytes)", Type: connectors.FilterNumber, Operators: connectors.NumberOps},
		{Key: "modifiedAt", Label: "Modified", Type: connectors.FilterDatetime, Operators: connectors.DatetimeOps},
	}
}

func newClient(cred connectors.Credential) (*minio.Client, error) {
	endpoint := cred.String("endpoint")
	secure := true
	host := "s3.amazonaws.com"
	if endpoint != "" {
		u, err := url.Parse(endpoint)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("invalid S3 endpoint")
		}
		host = u.Host
		secure = u.Scheme != "http"
	}
	lookup := minio.BucketLookupAuto
	if cred.Bool("pathStyle") {
		lookup = minio.BucketLookupPath
	}
	transport := httpx.Shared.Clone()
	return minio.New(host, &minio.Options{
		Creds:        credentials.NewStaticV4(cred.String("accessKeyId"), cred.String("secretAccessKey"), cred.String("sessionToken")),
		Secure:       secure,
		Region:       cred.String("region"),
		BucketLookup: lookup,
		Transport:    &headerTransport{base: transport, headers: cred.Headers},
	})
}

// ValidateCredential lists buckets (or falls back when not permitted).
func (c *Connector) ValidateCredential(ctx context.Context, cred connectors.Credential) (string, error) {
	cl, err := newClient(cred)
	if err != nil {
		return "", err
	}
	buckets, err := cl.ListBuckets(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Connected (%d buckets visible)", len(buckets)), nil
}

// ValidateConfig checks configuration.
func (c *Connector) ValidateConfig(cfg map[string]any, f connectors.Filter) error {
	if err := connectors.Require(cfg, "bucket"); err != nil {
		return err
	}
	return connectors.ValidateFilter(f, c.Info().FilterFields, false)
}

// Browse lists buckets at the root, then "folders" (common prefixes).
// ParentID is "bucket" or "bucket/prefix/".
func (c *Connector) Browse(ctx context.Context, cred connectors.Credential, req connectors.BrowseRequest) (connectors.BrowseResult, error) {
	cl, err := newClient(cred)
	if err != nil {
		return connectors.BrowseResult{}, err
	}
	res := connectors.BrowseResult{Breadcrumbs: []connectors.Breadcrumb{{ID: "", Name: "Buckets"}}}
	if req.ParentID == "" {
		bucket := connectors.AsString(req.Config["bucket"])
		if bucket == "" || req.Kind == connectors.KindBucket {
			buckets, err := cl.ListBuckets(ctx)
			if err != nil {
				return res, err
			}
			for _, b := range buckets {
				res.Items = append(res.Items, connectors.BrowseItem{ID: b.Name, Name: b.Name, Kind: connectors.KindBucket, Path: b.Name,
					HasChildren: req.Kind != connectors.KindBucket, Selectable: req.Kind == connectors.KindBucket})
			}
			return res, nil
		}
		req.ParentID = bucket
	}
	bucket, prefix, _ := strings.Cut(req.ParentID, "/")
	res.Breadcrumbs = append(res.Breadcrumbs, connectors.Breadcrumb{ID: bucket, Name: bucket})
	acc := ""
	for _, part := range strings.Split(strings.TrimSuffix(prefix, "/"), "/") {
		if part == "" {
			continue
		}
		acc += part + "/"
		res.Breadcrumbs = append(res.Breadcrumbs, connectors.Breadcrumb{ID: bucket + "/" + acc, Name: part})
	}
	count := 0
	for obj := range cl.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: false}) {
		if obj.Err != nil {
			return res, obj.Err
		}
		if count++; count > 1000 {
			break
		}
		if strings.HasSuffix(obj.Key, "/") {
			name := path.Base(strings.TrimSuffix(obj.Key, "/"))
			res.Items = append(res.Items, connectors.BrowseItem{ID: bucket + "/" + obj.Key, Name: name, Kind: connectors.KindFolder, Path: obj.Key, HasChildren: true, Selectable: req.Kind != connectors.KindBucket})
		} else {
			res.Items = append(res.Items, connectors.BrowseItem{ID: bucket + "/" + obj.Key, Name: path.Base(obj.Key), Kind: connectors.KindFile, Path: obj.Key})
		}
	}
	return res, nil
}

type opaque struct {
	Bucket    string `json:"b"`
	Key       string `json:"k"`
	VersionID string `json:"v,omitempty"`
}

// Scan performs a full metadata LIST (cheap) every run; only changed objects
// are downloaded, as decided by the engine's fingerprints.
func (c *Connector) Scan(ctx context.Context, req connectors.ScanRequest) (connectors.ScanResult, error) {
	cl, err := newClient(req.Credential)
	if err != nil {
		return connectors.ScanResult{}, err
	}
	bucket := connectors.AsString(req.Config["bucket"])
	prefix := connectors.AsString(req.Config["prefix"])
	// Browse IDs are "bucket/prefix"; accept them pasted into prefix.
	prefix = strings.TrimPrefix(prefix, bucket+"/")
	recursive := connectors.BoolDefault(req.Config, "recursive", true)

	for obj := range cl.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: recursive}) {
		if obj.Err != nil {
			return connectors.ScanResult{}, fmt.Errorf("list objects: %w", obj.Err)
		}
		if strings.HasSuffix(obj.Key, "/") {
			continue
		}
		rev := obj.VersionID
		if rev == "" || rev == "null" {
			rev = fmt.Sprintf("%s|%s|%d", strings.Trim(obj.ETag, `"`), obj.LastModified.UTC().Format("20060102T150405.000"), obj.Size)
		}
		op, _ := json.Marshal(opaque{Bucket: bucket, Key: obj.Key, VersionID: obj.VersionID})
		item := connectors.SourceItem{
			ID:         bucket + "/" + obj.Key,
			Name:       path.Base(obj.Key),
			Path:       obj.Key,
			Kind:       connectors.KindFile,
			Revision:   rev,
			ModifiedAt: obj.LastModified,
			MIMEType:   obj.ContentType,
			Size:       obj.Size,
			Metadata:   map[string]string{"s3_bucket": bucket, "s3_key": obj.Key, "file_name": path.Base(obj.Key)},
			Tags:       []string{"s3_bucket:" + bucket},
			Opaque:     op,
		}
		if err := req.Emit(item); err != nil {
			return connectors.ScanResult{}, err
		}
	}
	return connectors.ScanResult{Cursor: json.RawMessage(`{}`), Complete: true}, nil
}

// OpenContent streams the object.
func (c *Connector) OpenContent(ctx context.Context, req connectors.ContentRequest) (connectors.SourceContent, error) {
	cl, err := newClient(req.Credential)
	if err != nil {
		return connectors.SourceContent{}, err
	}
	var op opaque
	if err := json.Unmarshal(req.Item.Opaque, &op); err != nil {
		return connectors.SourceContent{}, err
	}
	opts := minio.GetObjectOptions{}
	if op.VersionID != "" && op.VersionID != "null" {
		opts.VersionID = op.VersionID
	}
	obj, err := cl.GetObject(ctx, op.Bucket, op.Key, opts)
	if err != nil {
		return connectors.SourceContent{}, err
	}
	// GetObject is lazy; Stat forces the request so errors surface here.
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		if minio.ToErrorResponse(err).StatusCode == 404 {
			return connectors.SourceContent{}, connectors.Skip("object no longer exists")
		}
		return connectors.SourceContent{}, err
	}
	return connectors.SourceContent{
		Body:      obj,
		FileName:  req.Item.Name,
		MIMEType:  req.Item.MIMEType,
		Size:      req.Item.Size,
		Context:   fmt.Sprintf("File %q from S3 bucket %q", op.Key, op.Bucket),
		Timestamp: req.Item.ModifiedAt,
	}, nil
}
