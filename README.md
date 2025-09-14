# BrokoliSQL-Go

BrokoliSQL-Go is a powerful command-line tool written in Go that converts structured data files (CSV, Excel, JSON, XML) into SQL INSERT statements. It provides flexible data transformation capabilities and supports multiple SQL dialects, making it ideal for database seeding, data migration, and ETL workflows.

![BrokoliSQL-Go](https://img.shields.io/badge/BrokoliSQL-Go-brightgreen)

## Features

- **Multi-format Support**: Process CSV, Excel (XLSX), JSON, and XML files
- **Remote Data Fetching**: Retrieve data directly from REST APIs and other remote sources
- **Nested JSON Support**: Automatically normalize nested JSON objects into proper relational tables
- **SQL Dialect Support**: Generate SQL for PostgreSQL, MySQL, SQLite, SQL Server, Oracle, and more
- **Automatic Table Creation**: Optionally generate CREATE TABLE statements based on input data
- **Smart Type Inference**: Automatically detect appropriate SQL data types
- **Batch Processing**: Control the number of rows per INSERT statement for optimal performance
- **Powerful Transformations**: Apply various transformations to your data before SQL generation
- **Column Normalization**: Automatically normalize column names for SQL compatibility
- **Streaming Processing** (Experimental): Process large files with constant memory usage

## Installation

### Prerequisites

- Go 1.24 or higher

### From Source

```bash
# Clone the repository
git clone https://github.com/hc12r/brokolisql-go.git
cd brokolisql-go

# Build the binary
go build -o brokolisql

# Install globally (optional)
go install
```

## Usage

### Basic Usage

```bash
brokolisql --input data.csv --output output.sql --table users
```

### Command-Line Options

```
Usage:
  brokolisql [flags]

Flags:
  -b, --batch-size int       Number of rows per INSERT statement (default 100)
  -c, --create-table         Generate CREATE TABLE statement
  -d, --dialect string       SQL dialect (generic, postgres, mysql, sqlite, sqlserver, oracle) (default "generic")
      --fetch                Enable fetch mode to retrieve data from remote sources
  -f, --format string        Input file format (csv, json, xml, xlsx) - if not specified, will be inferred from file extension
  -h, --help                 help for brokolisql
  -i, --input string         Input file path (required unless using fetch mode)
      --log-level string     Log level (debug, info, warning, error, fatal) (default "info")
  -n, --normalize            Normalize column names for SQL compatibility (default true)
  -o, --output string        Output SQL file path (required)
      --source string        Source URL or connection string for fetch mode
      --source-type string   Source type for fetch mode (rest, etc.) (default "rest")
  -r, --transform string     JSON file with transformation rules
  -t, --table string         Table name for SQL statements (required)
      --streaming            Enable streaming mode for processing large files with constant memory usage
      --buffer-size int      Number of rows to buffer in memory when using streaming mode (default 1000)
```

### Examples

Generate SQL with a specific dialect:

```bash
brokolisql --input data.csv --output output.sql --table users --dialect mysql
```

Generate a CREATE TABLE statement:

```bash
brokolisql --input data.csv --output output.sql --table users --create-table
```

Use batch inserts for better performance:

```bash
brokolisql --input data.csv --output output.sql --table users --batch-size 100
```

Apply transformations:

```bash
brokolisql --input data.csv --output output.sql --table users --transform transforms.json
```

Fetch data from a REST API:

```bash
brokolisql --fetch --source https://api.example.com/data --output output.sql --table users
```

Fetch data and apply transformations:

```bash
brokolisql --fetch --source https://api.example.com/data --output output.sql --table users --transform transforms.json
```

Process a large file with streaming mode:

```bash
brokolisql --input large_file.json --output output.sql --table users --streaming --buffer-size 1000 --create-table
```

## Remote Data Fetching

BrokoliSQL-Go can fetch data directly from remote sources, eliminating the need to download files locally before processing. Currently, it supports:

- **REST APIs**: Fetch JSON data from HTTP endpoints

### Using Fetch Mode

To use fetch mode, use the `--fetch` flag along with the following options:

- `--source`: The URL or connection string for the data source
- `--source-type`: The type of source (currently supports "rest")

Example:

```bash
brokolisql --fetch --source https://api.example.com/users --source-type rest --output users.sql --table users
```

### REST API Options

When fetching from REST APIs, the following default settings are applied:

- HTTP Method: GET
- Accept Header: application/json

The fetched JSON data is automatically parsed and converted to the same internal format used by the file loaders, allowing you to apply transformations and generate SQL just like with local files.

## Logging

BrokoliSQL-Go provides comprehensive logging capabilities:

- **Log Levels**: Control verbosity with different log levels (debug, info, warning, error, fatal)
- **File Logging**: All logs are written to `brok.log` for troubleshooting and auditing
- **Console Output**: Only important messages are displayed in the console
- **In-place Updates**: Progress messages use carriage returns for cleaner console output
- **Security**: Sensitive data is only logged at debug level and only to the log file

### Using Logging Options

To control logging behavior, use the `--log-level` flag:

```bash
# Minimal logging (only errors and fatal messages)
brokolisql --input data.csv --output output.sql --table users --log-level error

# Default logging level (informational messages)
brokolisql --input data.csv --output output.sql --table users --log-level info

# Verbose logging for troubleshooting (includes sensitive data in log file)
brokolisql --input data.csv --output output.sql --table users --log-level debug
```

All logs are written to `brok.log` in the current directory, regardless of the console output level.

## Streaming Processing (Experimental)

BrokoliSQL-Go includes an experimental streaming mode that processes large files with constant memory usage, allowing you to handle files of any size without loading the entire file into memory.

### Using Streaming Mode

To use streaming mode, use the `--streaming` flag along with the following options:

- `--buffer-size`: Number of rows to buffer in memory (default: 1000)

Example:

```bash
brokolisql --input large_file.json --output output.sql --table users --streaming --buffer-size 1000
```

### Supported Formats

Currently, streaming mode supports:

- **CSV files**: Process CSV files line by line
- **JSON files**: Process JSON files incrementally
- **Nested JSON**: Automatically detect and normalize nested JSON objects into proper relational tables

### Limitations

As this is an experimental feature:

- Excel and XML files are not yet supported in streaming mode
- Memory usage may still spike when processing nested JSON objects
- Performance optimizations are ongoing

## Data Transformations

BrokoliSQL-Go supports powerful data transformations through a JSON configuration file. Here's an example:

```json
{
  "transformations": [
    {
      "type": "rename_columns",
      "mapping": {
        "FIRST_NAME": "GIVEN_NAME",
        "LAST_NAME": "SURNAME"
      }
    },
    {
      "type": "add_column",
      "name": "FULL_NAME",
      "expression": "GIVEN_NAME + ' ' + SURNAME"
    },
    {
      "type": "filter_rows",
      "condition": "COUNTRY in ['USA', 'Canada', 'UK', 'Germany']"
    },
    {
      "type": "apply_function",
      "column": "EMAIL",
      "function": "lower"
    },
    {
      "type": "replace_values",
      "column": "COUNTRY",
      "mapping": {
        "USA": "United States",
        "UK": "United Kingdom"
      }
    },
    {
      "type": "drop_columns",
      "columns": ["TEMP_COLUMN"]
    },
    {
      "type": "sort",
      "columns": ["COUNTRY", "CITY"],
      "ascending": true
    }
  ]
}
```

## Use Cases

BrokoliSQL-Go is particularly useful in the following scenarios:

### Data Migration

When migrating data between systems, BrokoliSQL-Go can transform source data into SQL statements compatible with the target database, handling format conversions and data transformations in a single step.

### Database Seeding

For development and testing environments, BrokoliSQL-Go makes it easy to convert sample data from various formats into SQL for database initialization.

### ETL Workflows

As part of Extract-Transform-Load (ETL) pipelines, BrokoliSQL-Go can transform data from various sources and prepare it for loading into a database.

### Data Analysis

Data analysts can use BrokoliSQL-Go to quickly convert data from various formats into SQL for further analysis in a database environment.

### API Integration

When integrating with APIs that provide data in JSON or XML format, BrokoliSQL-Go can transform this data into SQL for storage in a relational database.

## Continuous Integration and GitHub Actions

![Tests](https://github.com/hc12r/brokolisql-go/actions/workflows/tests.yml/badge.svg)
![Code Quality](https://github.com/hc12r/brokolisql-go/actions/workflows/code-quality.yml/badge.svg)
![Security Scan](https://github.com/hc12r/brokolisql-go/actions/workflows/security.yml/badge.svg)

BrokoliSQL-Go uses GitHub Actions for continuous integration, testing, and deployment. The following workflows are available:

- **Tests**: Runs the test suite and generates code coverage reports
- **Code Quality**: Checks code quality using linters and formatters
- **Cross-Platform Tests**: Ensures the code works across different operating systems and Go versions
- **Release Automation**: Automates the release process when a new version is tagged
- **Dependency Updates**: Keeps dependencies up-to-date with weekly checks
- **Security Scan**: Scans the code for security vulnerabilities

For more details on how to use and customize these workflows, see [GITHUB_ACTIONS.md](docs/GITHUB_ACTIONS.md).  
For a summary of the GitHub Actions implementation, benefits, and next steps, see [GITHUB_ACTIONS_SUMMARY.md](docs/GITHUB_ACTIONS_SUMMARY.md).  
For a comprehensive overview of the entire implementation, see [IMPLEMENTATION_SUMMARY.md](docs/IMPLEMENTATION_SUMMARY.md).

## Project Structure

```
brokolisql-go/
├── brokolisql-go.iml
├── cmd
│   └── root.go
├── docs
│   ├── GITHUB_ACTIONS.md
│   ├── GITHUB_ACTIONS_SUMMARY.md
│   └── IMPLEMENTATION_SUMMARY.md
├── examples
│   ├── customers.csv
│   ├── output-100.sql
│   ├── output.sql
│   ├── output_transformed.sql
│   └── transforms.json
├── internal
│   ├── dialects
│   │   ├── dialect.go
│   │   ├── dialect_test.go
│   │   ├── generic.go
│   │   ├── generic_test.go
│   │   ├── mysql.go
│   │   ├── oracle.go
│   │   ├── postgres.go
│   │   ├── sqlite.go
│   │   └── sqlserver.go
│   ├── processing
│   │   ├── normalizer.go
│   │   ├── normalizer_test.go
│   │   ├── sql_generator.go
│   │   ├── sql_generator_test.go
│   │   ├── type_inference.go
│   │   └── type_inference_test.go
│   └── transformers
│       ├── transform_engine.go
│       └── transform_engine_test.go
├── pkg
│   ├── common
│   │   ├── logger.go
│   │   └── logger_test.go
│   ├── errors
│   │   ├── errors.go
│   │   ├── errors_test.go
│   │   ├── handler.go
│   │   └── handler_test.go
│   └── loaders
│       ├── csv_loader.go
│       ├── csv_loader_test.go
│       ├── excel_loader.go
│       ├── excel_loader_test.go
│       ├── json_loader.go
│       ├── json_loader_test.go
│       ├── loader.go
│       ├── loader_test.go
│       ├── xml_loader.go
│       └── xml_loader_test.go
├── go.mod
├── go.sum
├── main.go
├── LICENSE
└── README.md
```

## Contributing

Contributions to BrokoliSQL-Go are welcome! Here's how you can contribute:

1. **Fork the Repository**: Create your own fork of the project.

2. **Create a Feature Branch**:
   ```bash
   git checkout -b feature/your-feature-name
   ```

3. **Make Your Changes**: Implement your feature or bug fix.

4. **Write Tests**: Add tests for your changes to ensure they work correctly.

5. **Run Tests**:
   ```bash
   go test ./...
   ```

6. **Format Your Code**:
   ```bash
   go fmt ./...
   ```

7. **Commit Your Changes**:
   ```bash
   git commit -m "Add feature: your feature description"
   ```

8. **Push to Your Fork**:
   ```bash
   git push origin feature/your-feature-name
   ```

9. **Create a Pull Request**: Open a pull request from your fork to the main repository.

### Development Guidelines

- Follow Go best practices and coding conventions
- Write clear, concise commit messages
- Document your code with comments
- Add unit tests for new functionality
- Update documentation when necessary

## License

BrokoliSQL-Go is licensed under the GNU GPL-3.0 License. See the [LICENSE](LICENSE) file for details.

## Acknowledgments

BrokoliSQL-Go is a Go implementation of the original [BrokoliSQL](https://github.com/hc12r/brokolisql) Python project, reimagined with Go's performance and concurrency benefits.
