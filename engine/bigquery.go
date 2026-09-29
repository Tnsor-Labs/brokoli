package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	googlehttp "cloud.google.com/go/auth/httptransport"
	"cloud.google.com/go/bigquery"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/dbdialect"
	"github.com/Tnsor-Labs/brokoli/pkg/identity"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

const bigQueryDefaultMaxBytes = int64(10 << 30)

func isBigQueryURI(uri string) bool {
	return strings.HasPrefix(uri, "bigquery://")
}

// bigQueryProjectID is a Google Cloud project ID, optionally prefixed by a
// domain as older projects are ("example.com:project").
var bigQueryProjectID = regexp.MustCompile(`^([a-z0-9][a-z0-9.-]*[a-z0-9]:)?[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// bigQueryQuotaProject returns the URI's billing_project, the project that
// pays for and is charged quota for the jobs, or "" to bill the resource
// project. It becomes a request header, so it must look like a project ID.
func bigQueryQuotaProject(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid BigQuery URI")
	}
	project := u.Query().Get("billing_project")
	if project != "" && !bigQueryProjectID.MatchString(project) {
		return "", fmt.Errorf("BigQuery billing_project %q is not a Google Cloud project ID", project)
	}
	return project, nil
}

// bigQueryClient opens a client for uri with the connection's settings.
//
// settings is the connection's extra document, resolved where the node runs
// by bigQuerySettings: a service-account key, or an object carrying one under
// "credentials" beside the other settings. It is passed here and nowhere
// else, so the key never enters a node's config, a log line or a work order
// (ADR-042 section 1). Empty settings mean the machine's own identity, which
// is refused where ambient identity is denied.
func bigQueryClient(ctx context.Context, uri string, auth googleAuth) (*bigquery.Client, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid BigQuery URI")
	}
	project := u.Hostname()
	if project == "" {
		return nil, fmt.Errorf("BigQuery URI has no project")
	}

	// Every request, and every token fetch, goes through the outbound policy.
	httpClient := netguard.Outbound().Client(30 * time.Minute)
	if endpoint := os.Getenv("BROKOLI_BIGQUERY_ENDPOINT"); endpoint != "" {
		// Test-only endpoint for the local emulator. The production path never
		// uses an endpoint from connection data, and the emulator has no auth.
		return bigquery.NewClient(ctx, project, option.WithHTTPClient(httpClient),
			option.WithEndpoint(endpoint), option.WithoutAuthentication())
	}

	creds, err := googleCredentials("BigQuery", auth, httpClient, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, err
	}
	// The credentials have to be applied to the HTTP client itself.
	// option.WithHTTPClient "takes precedent over all other supplied
	// options", so passing it beside WithAuthCredentials sent every request
	// without credentials. Google's authenticating transport is layered over
	// the outbound-policy transport instead, and that one client is all the
	// BigQuery client gets.
	// The billing project is a header on the authenticated client, where
	// Google's transport reads it as the quota project. It cannot be
	// option.WithQuotaProject: beside WithHTTPClient the library refuses to
	// build a client at all ("WithHTTPClient is incompatible with
	// QuotaProject"). An explicit billing_project takes precedence over a
	// quota project embedded in a service-account key.
	quotaProject, err := bigQueryQuotaProject(uri)
	if err != nil {
		return nil, err
	}
	headers := http.Header{}
	if quotaProject != "" {
		headers.Set("X-Goog-User-Project", quotaProject)
	}
	authClient, err := googlehttp.NewClient(&googlehttp.Options{
		BaseRoundTripper: httpClient.Transport,
		Credentials:      creds,
		Headers:          headers,
	})
	if err != nil {
		return nil, fmt.Errorf("BigQuery authenticated client: %w", err)
	}
	authClient.Timeout = httpClient.Timeout
	opts := []option.ClientOption{option.WithHTTPClient(authClient)}
	if bigQueryAPIEndpoint != "" {
		opts = append(opts, option.WithEndpoint(bigQueryAPIEndpoint))
	}
	return bigquery.NewClient(ctx, project, opts...)
}

// bigQueryAPIEndpoint replaces BigQuery's API endpoint on the authenticated
// path. Tests only, to see the credentials a request carries; a connection
// cannot set it.
var bigQueryAPIEndpoint string

func bigQueryQueryConfig(client *bigquery.Client, uri, query string, config map[string]interface{}, settings string) (*bigquery.Query, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("invalid BigQuery URI: %w", err)
	}
	q := client.Query(query)
	q.UseLegacySQL = false
	q.MaxBytesBilled = bigQueryDefaultMaxBytes
	q.Labels = map[string]string{"brokoli": "true"}
	q.DefaultProjectID = u.Hostname()
	if dataset := strings.TrimPrefix(u.Path, "/"); dataset != "" {
		q.DefaultDatasetID = dataset
	}
	if value := u.Query().Get("location"); value != "" {
		q.Location = value
	}
	if value, ok := config["run_id"].(string); ok && value != "" {
		q.Labels["brokoli_run"] = value
	}
	if value, ok := config["node_id"].(string); ok && value != "" {
		q.Labels["brokoli_node"] = value
	}
	if settings != "" {
		var parsed map[string]interface{}
		if json.Unmarshal([]byte(settings), &parsed) == nil {
			if value, ok := parsed["maximum_bytes_billed"].(float64); ok && value >= 1 {
				q.MaxBytesBilled = int64(value)
			}
		}
	}
	return q, nil
}

// DryRunBigQuery validates a query and returns its result schema without
// scanning or charging for data. The byte estimate is retained for the caller
// to apply a cost policy before the real query runs.
func DryRunBigQuery(ctx context.Context, uri, query string, config map[string]interface{}, auth googleAuth) (bigquery.Schema, int64, error) {
	client, err := bigQueryClient(ctx, uri, auth)
	if err != nil {
		return nil, 0, err
	}
	defer client.Close()
	q, err := bigQueryQueryConfig(client, uri, query, config, auth.settings)
	if err != nil {
		return nil, 0, err
	}
	q.DryRun = true
	job, err := q.Run(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("BigQuery dry run: %w", err)
	}
	status := job.LastStatus()
	if status == nil {
		return nil, 0, fmt.Errorf("BigQuery dry run returned no status")
	}
	if err := status.Err(); err != nil {
		return nil, 0, fmt.Errorf("BigQuery dry run: %w", err)
	}
	if status.Statistics == nil {
		return nil, 0, fmt.Errorf("BigQuery dry run returned no statistics")
	}
	stats, ok := status.Statistics.Details.(*bigquery.QueryStatistics)
	if !ok || stats == nil {
		return nil, status.Statistics.TotalBytesProcessed, fmt.Errorf("BigQuery dry run returned no query schema")
	}
	return stats.Schema, stats.TotalBytesProcessed, nil
}

func bigQueryColumnSchema(schema bigquery.Schema) columnSchema {
	out := make(columnSchema, len(schema))
	for _, field := range schema {
		ct := dbdialect.ColumnType{Nullable: !field.Required}
		switch strings.ToUpper(string(field.Type)) {
		case "STRING", "GEOGRAPHY", "JSON":
			ct.Class = dbdialect.TypeText
		case "BOOL", "BOOLEAN":
			ct.Class = dbdialect.TypeBool
		case "INT64", "INTEGER":
			ct.Class, ct.Bits = dbdialect.TypeInt, 64
		case "FLOAT64", "FLOAT":
			ct.Class, ct.Bits = dbdialect.TypeFloat, 64
		case "NUMERIC", "BIGNUMERIC":
			ct.Class = dbdialect.TypeDecimal
		case "BYTES":
			ct.Class = dbdialect.TypeBytes
		case "DATE":
			ct.Class = dbdialect.TypeDate
		case "DATETIME", "TIME":
			ct.Class = dbdialect.TypeTimestamp
		case "TIMESTAMP":
			ct.Class = dbdialect.TypeTimestampTZ
		default:
			ct.Class = dbdialect.TypeUnknown
		}
		out[field.Name] = ct
	}
	return out
}

func bigQueryLoadSchema(data *common.DataSet) bigquery.Schema {
	fields := make(bigquery.Schema, 0, len(data.Columns))
	for _, name := range data.Columns {
		field := &bigquery.FieldSchema{Name: name, Type: bigquery.StringFieldType}
		for _, row := range data.Rows {
			value := row[name]
			if value == nil {
				continue
			}
			switch value.(type) {
			case int, int8, int16, int32, int64, uint8, uint16, uint32:
				field.Type = bigquery.IntegerFieldType
			case float32, float64:
				field.Type = bigquery.FloatFieldType
			case bool:
				field.Type = bigquery.BooleanFieldType
			case []byte:
				field.Type = bigquery.BytesFieldType
			case time.Time:
				field.Type = bigquery.TimestampFieldType
			}
			break
		}
		fields = append(fields, field)
	}
	return fields
}

func QueryBigQuery(ctx context.Context, uri, query string, config map[string]interface{}, auth googleAuth) (*common.DataSet, error) {
	client, err := bigQueryClient(ctx, uri, auth)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	q, err := bigQueryQueryConfig(client, uri, query, config, auth.settings)
	if err != nil {
		return nil, err
	}
	it, err := q.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("BigQuery query: %w", err)
	}
	columns := make([]string, 0, len(it.Schema))
	for _, field := range it.Schema {
		columns = append(columns, field.Name)
	}
	if len(columns) == 0 {
		if schema, _, dryErr := DryRunBigQuery(ctx, uri, query, config, auth); dryErr == nil {
			for _, field := range schema {
				columns = append(columns, field.Name)
			}
		}
	}
	rows := make([]common.DataRow, 0)
	for {
		var values []bigquery.Value
		if err := it.Next(&values); err == iterator.Done {
			break
		} else if err != nil {
			return nil, fmt.Errorf("BigQuery rows: %w", err)
		}
		if len(columns) == 0 {
			columns = make([]string, len(values))
			for i := range values {
				columns[i] = fmt.Sprintf("column_%d", i+1)
			}
		}
		row := make(common.DataRow, len(columns))
		for i, value := range values {
			row[columns[i]] = value
		}
		rows = append(rows, row)
	}
	return &common.DataSet{Columns: columns, Rows: rows}, nil
}

// CheckBigQueryConnection runs SELECT 1 with the connection's settings. For
// auth_method "oidc" it takes a token from tokens with req, so a connection
// test exercises the same federation a run does.
func CheckBigQueryConnection(ctx context.Context, uri string, config map[string]interface{}, settings string, tokens identity.TokenSource, req identity.TokenRequest) error {
	auth := googleAuth{settings: settings, tokens: tokens, request: req}
	client, err := bigQueryClient(ctx, uri, auth)
	if err != nil {
		return err
	}
	defer client.Close()
	q, err := bigQueryQueryConfig(client, uri, "SELECT 1", config, auth.settings)
	if err != nil {
		return err
	}
	job, err := q.Run(ctx)
	if err != nil {
		return fmt.Errorf("BigQuery connection test: %w", err)
	}
	status, err := job.Wait(ctx)
	if err != nil {
		return fmt.Errorf("BigQuery connection test: %w", err)
	}
	return status.Err()
}

func LoadBigQuery(ctx context.Context, uri, table, mode string, data *common.DataSet, config map[string]interface{}, auth googleAuth) error {
	if strings.EqualFold(strings.TrimSpace(mode), ModeUpsert) {
		return fmt.Errorf("BigQuery does not support upsert in this build")
	}
	u, err := url.Parse(uri)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("invalid BigQuery URI")
	}
	dataset := strings.TrimPrefix(u.Path, "/")
	if dataset == "" {
		return fmt.Errorf("BigQuery URI has no dataset")
	}
	client, err := bigQueryClient(ctx, uri, auth)
	if err != nil {
		return err
	}
	defer client.Close()

	reader, writer := io.Pipe()
	go func() {
		enc := json.NewEncoder(writer)
		for _, row := range data.Rows {
			if err := enc.Encode(row); err != nil {
				_ = writer.CloseWithError(err)
				return
			}
		}
		_ = writer.Close()
	}()
	source := bigquery.NewReaderSource(reader)
	source.SourceFormat = bigquery.JSON
	source.Schema = bigQueryLoadSchema(data)
	loader := client.Dataset(dataset).Table(table).LoaderFrom(source)
	loader.CreateDisposition = bigquery.CreateIfNeeded
	loader.WriteDisposition = bigquery.WriteAppend
	if strings.EqualFold(strings.TrimSpace(mode), ModeOverwrite) || strings.EqualFold(strings.TrimSpace(mode), "replace") {
		loader.WriteDisposition = bigquery.WriteTruncateData
	}
	loader.Labels = map[string]string{"brokoli": "true"}
	loader.Location = u.Query().Get("location")
	if value, ok := config["run_id"].(string); ok && value != "" {
		loader.Labels["brokoli_run"] = value
	}
	job, err := loader.Run(ctx)
	if err != nil {
		return fmt.Errorf("BigQuery load: %w", err)
	}
	status, err := job.Wait(ctx)
	if err != nil {
		return fmt.Errorf("BigQuery load wait: %w", err)
	}
	if status.Err() != nil {
		return fmt.Errorf("BigQuery load: %w", status.Err())
	}
	return nil
}
