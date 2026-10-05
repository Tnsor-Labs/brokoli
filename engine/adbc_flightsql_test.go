//go:build adbc

package engine

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/flight"
	flightserver "github.com/apache/arrow-go/v18/arrow/flight/flightsql"
	flightsqlsqlite "github.com/apache/arrow-go/v18/arrow/flight/flightsql/example"
	_ "modernc.org/sqlite"
)

func TestStreamFlightSQLToArrowIPC(t *testing.T) {
	db, err := sql.Open("sqlite", "file:adbc_flightsql_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE widgets (id INTEGER, name TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO widgets VALUES (1, 'one'), (2, 'two')"); err != nil {
		t.Fatal(err)
	}

	service, err := flightsqlsqlite.NewSQLiteFlightSQLServer(db)
	if err != nil {
		t.Fatal(err)
	}
	server := flight.NewServerWithMiddleware(nil)
	server.RegisterFlightService(flightserver.NewFlightServer(service))
	if err := server.Init("localhost:0"); err != nil {
		t.Fatal(err)
	}
	defer server.Shutdown()
	go func() { _ = server.Serve() }()

	var stream bytes.Buffer
	columns, rows, err := StreamFlightSQLToArrowIPC(context.Background(), "grpc+tcp://"+server.Addr().String(), "SELECT id, name FROM widgets ORDER BY id", nil, &stream)
	if err != nil {
		t.Fatalf("StreamFlightSQLToArrowIPC: %v", err)
	}
	if got, want := rows, int64(2); got != want {
		t.Errorf("rows = %d, want %d", got, want)
	}
	if got, want := len(columns), 2; got != want || columns[0] != "id" || columns[1] != "name" {
		t.Errorf("columns = %v, want [id name]", columns)
	}

	reader, err := NewArrowBatchReader(&stream, columns)
	if err != nil {
		t.Fatalf("NewArrowBatchReader: %v", err)
	}
	defer reader.Release()
	batch, err := reader.Next()
	if err != nil {
		t.Fatalf("read Arrow batch: %v", err)
	}
	if got, want := len(batch.Rows), 2; got != want {
		t.Fatalf("rows = %v, want %d rows", got, want)
	}
	if got, want := batch.Rows[0]["id"], int64(1); got != want {
		t.Errorf("first id = %#v, want %#v", got, want)
	}
	if got, want := batch.Rows[1]["name"], "two"; got != want {
		t.Errorf("second name = %#v, want %#v", got, want)
	}
	if _, err := reader.Next(); err != io.EOF {
		t.Errorf("reader.Next() error = %v, want io.EOF", err)
	}
}
