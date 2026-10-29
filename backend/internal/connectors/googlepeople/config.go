// Package googlepeople ingests saved Google Contacts as Markdown documents.
package googlepeople

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/Nyrest/hindsight-ingestion/internal/connectors"
	"github.com/Nyrest/hindsight-ingestion/internal/googleauth"
	"github.com/Nyrest/hindsight-ingestion/internal/httpx"
	"gorm.io/gorm"
)

const SourceType = "google_people"
const personFields = "metadata,names,nicknames,emailAddresses,phoneNumbers,organizations,addresses,birthdays,events,relations,biographies,urls,memberships"

var apiBase = "https://people.googleapis.com/v1"

type Connector struct{ DB *gorm.DB }

func init() {
	connectors.RegisterCredentialType(connectors.CredentialType{
		Type: SourceType, Name: "Google People", OAuth: true,
		Description: "Google account via OAuth",
		Fields: []connectors.FieldSpec{
			{Key: "clientId", Label: "OAuth client ID", Type: connectors.FieldString, Required: true},
			{Key: "clientSecret", Label: "OAuth client secret", Type: connectors.FieldPassword, Secret: true, Required: true,
				Help: "Enable People API in your Google Cloud project. Reconnecting must use the same Google account."},
		},
	})
	connectors.RegisterSource(&Connector{})
}

func (*Connector) Info() connectors.SourceInfo {
	return connectors.SourceInfo{
		Type: SourceType, Name: "Google People", CredentialTypes: []string{SourceType},
		BrowseKinds: []string{}, Fields: []connectors.FieldSpec{},
		Capabilities: connectors.Capabilities{SupportsOAuth: true, IncrementalMode: connectors.IncrementalSyncToken, DeletionMode: connectors.DeletionDelta},
		FilterFields: []connectors.FilterFieldSpec{
			{Key: "name", Label: "Contact name", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "email", Label: "Email address", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "phone", Label: "Phone number", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "organization", Label: "Organization", Type: connectors.FilterString, Operators: connectors.StringOps},
			{Key: "modifiedAt", Label: "Modified", Type: connectors.FilterDatetime, Operators: connectors.DatetimeOps},
		},
	}
}

func (*Connector) Browse(context.Context, connectors.Credential, connectors.BrowseRequest) (connectors.BrowseResult, error) {
	return connectors.BrowseResult{Items: []connectors.BrowseItem{}, Breadcrumbs: []connectors.Breadcrumb{}}, nil
}

func (c *Connector) ValidateConfig(_ map[string]any, filter connectors.Filter) error {
	return connectors.ValidateFilter(filter, c.Info().FilterFields, false)
}

func client(cred connectors.Credential) *httpx.Client {
	token := ""
	if cred.OAuth != nil {
		token = cred.OAuth.AccessToken
	}
	cl := httpx.New(cred.Headers, map[string]string{"Authorization": "Bearer " + token}, cred.Proxy)
	cl.Token = cred.AccessToken
	return cl
}

func (*Connector) ValidateCredential(ctx context.Context, cred connectors.Credential) (string, error) {
	subject, err := googleauth.Subject(ctx, cred)
	if err != nil {
		return "", err
	}
	if subject != cred.OAuthSubject {
		return "", errors.New("Google account changed; create a new credential and task")
	}
	query := url.Values{"personFields": {"metadata"}, "pageSize": {"1"}, "sources": {"READ_SOURCE_TYPE_CONTACT"}}
	var response struct{}
	if err := client(cred).JSON(ctx, http.MethodGet, apiBase+"/people/me/connections?"+query.Encode(), nil, &response); err != nil {
		return "", err
	}
	return "Connected to Google Contacts (read-only)", nil
}
