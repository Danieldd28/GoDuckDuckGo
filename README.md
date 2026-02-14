# GoDuckDuckGo

A Model Context Protocol (MCP) server for DuckDuckGo Search, written in Go.

## Features

- Native Go implementation
- Low resource usage
- **SSE (Server-Sent Events) Transport Support**
- DNS over HTTPS support for reliable connectivity
- Advanced search operators supported
- Rate limiting to prevent IP blocking

## Installation

Requirements: Go 1.21+

```bash
go build -ldflags="-s -w" -o GoDuckDuckGo main.go
```

## SSE Mode

Run with the `-addr` flag to start an SSE server:

```bash
./GoDuckDuckGo -addr :8080
```

This will start an SSE server on port 8080. The server exposes:
- `/sse`: SSE endpoint
- `/messages`: POST endpoint for client messages

## Usage

Add the following to your mcp.json configuration:

```json
{
    "mcpServers": {
        "go-ddg": {
            "command": "/absolute/path/to/GoDuckDuckGo",
            "args": []
        }
    }
}
```

## Tools

### search

Search DuckDuckGo.

Parameters:
- query (string): Search terms
- max_results (number): Default 10
- safe_search (string): strict, moderate (default), or off

### fetch_content

Fetch and clean webpage content.

Parameters:
- url (string): URL to fetch
